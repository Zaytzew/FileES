package recoverykit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"filees/pkg/deploy"
	"filees/pkg/repoworker"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	recoveryUser        = "_filees-recovery"
	recoveryCommand     = "filees recovery-v1"
	maxManifestResponse = 1 << 20
)

func Download(ctx context.Context, kit Kit, outputRoot string, now time.Time) ([]string, error) {
	if err := kit.Validate(now); err != nil {
		return nil, err
	}
	if !now.UTC().Before(kit.Manifest.DownloadUntil) {
		return nil, errors.New("automatic recovery download has expired; contact the server administrator")
	}
	if !filepath.IsAbs(outputRoot) {
		return nil, errors.New("recovery output directory must be absolute")
	}
	if err := os.MkdirAll(outputRoot, 0o700); err != nil {
		return nil, err
	}
	host, port, err := deploy.NormalizeServerAddress(kit.ServerAddress)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	signer, err := ssh.ParsePrivateKey([]byte(kit.PrivateKey))
	if err != nil {
		return nil, err
	}
	hostCallback, err := pinnedRecoveryHost(kit.KnownHost, address)
	if err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connect recovery server: %w", err)
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	sshConfig := &ssh.ClientConfig{
		User: recoveryUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, HostKeyCallback: hostCallback,
		Timeout: 30 * time.Second,
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("recovery SSH handshake: %w", err)
	}
	client := ssh.NewClient(clientConnection, channels, requests)
	defer client.Close()

	serverManifest, err := fetchRecoveryManifest(client, kit.OperationID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(serverManifest, kit.Manifest) {
		return nil, errors.New("server recovery manifest differs from the signed-off kit")
	}
	var paths []string
	for _, archive := range kit.Manifest.Archives {
		path, err := downloadRecoveryArchive(client, outputRoot, kit.OperationID, archive)
		if err != nil {
			return paths, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func fetchRecoveryManifest(client *ssh.Client, operationID string) (repoworker.RecoveryManifest, error) {
	session, err := client.NewSession()
	if err != nil {
		return repoworker.RecoveryManifest{}, err
	}
	defer session.Close()
	session.Stdin = strings.NewReader("list " + operationID + "\n")
	stdout, err := session.StdoutPipe()
	if err != nil {
		return repoworker.RecoveryManifest{}, err
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Start(recoveryCommand); err != nil {
		return repoworker.RecoveryManifest{}, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(stdout, maxManifestResponse+1))
	waitErr := session.Wait()
	if waitErr != nil {
		return repoworker.RecoveryManifest{}, fmt.Errorf("recovery manifest request failed: %w: %s", waitErr, stderr.String())
	}
	if readErr != nil {
		return repoworker.RecoveryManifest{}, readErr
	}
	if len(raw) > maxManifestResponse {
		return repoworker.RecoveryManifest{}, errors.New("recovery manifest response exceeds 1 MiB")
	}
	var manifest repoworker.RecoveryManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return repoworker.RecoveryManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return repoworker.RecoveryManifest{}, errors.New("recovery manifest contains trailing data")
	}
	if err := manifest.Validate(); err != nil || manifest.OperationID != operationID {
		return repoworker.RecoveryManifest{}, errors.New("server returned an invalid recovery manifest")
	}
	return manifest, nil
}

func downloadRecoveryArchive(client *ssh.Client, outputRoot, operationID string, archive repoworker.RecoveryArchive) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	session.Stdin = strings.NewReader("get " + operationID + " " + archive.ArchiveID + "\n")
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Start(recoveryCommand); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(outputRoot, "."+archive.RepoID+"-*.svndump.tmp")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(stdout, archive.Size+1))
	waitErr := session.Wait()
	if copyErr == nil && waitErr != nil {
		copyErr = fmt.Errorf("recovery archive request failed: %w: %s", waitErr, stderr.String())
	}
	if copyErr == nil && written != archive.Size {
		copyErr = fmt.Errorf("recovery archive size mismatch: got %d, want %d", written, archive.Size)
	}
	if copyErr == nil && hex.EncodeToString(hash.Sum(nil)) != archive.SHA256 {
		copyErr = errors.New("recovery archive SHA-256 mismatch")
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	if closeErr := temp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return "", copyErr
	}
	finalPath := filepath.Join(outputRoot, archive.RepoID+".svndump")
	if err := os.Link(tempPath, finalPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("recovery output already exists: %s", finalPath)
		}
		return "", err
	}
	if err := os.Remove(tempPath); err != nil {
		return "", err
	}
	directory, err := os.Open(outputRoot)
	if err != nil {
		return "", err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return finalPath, nil
}

func pinnedRecoveryHost(line, address string) (ssh.HostKeyCallback, error) {
	marker, hosts, publicKey, _, rest, err := ssh.ParseKnownHosts([]byte(strings.TrimSpace(line) + "\n"))
	if err != nil || marker != "" || len(hosts) != 1 || len(bytes.TrimSpace(rest)) != 0 || publicKey.Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("recovery kit host pin is invalid")
	}
	if strings.ContainsAny(hosts[0], "*?!|") || knownhosts.Normalize(hosts[0]) != knownhosts.Normalize(address) {
		return nil, errors.New("recovery kit host pin does not match server address")
	}
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		if knownhosts.Normalize(hostname) != knownhosts.Normalize(address) || !bytes.Equal(key.Marshal(), publicKey.Marshal()) {
			return errors.New("recovery server host key mismatch")
		}
		return nil
	}, nil
}

func validateRecoveryEndpoint(address, knownHost string) error {
	host, port, err := deploy.NormalizeServerAddress(address)
	if err != nil {
		return err
	}
	_, err = pinnedRecoveryHost(knownHost, net.JoinHostPort(host, strconv.Itoa(port)))
	return err
}
