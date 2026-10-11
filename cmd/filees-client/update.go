package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"filees/internal/clientupdate"
	"filees/internal/durable"
	"filees/internal/releaseenvelope"
	"filees/internal/serverinstall/svnfetch"
)

// Public trust and routing are injected together by the CLI release producer.
// Development builds deliberately cannot bootstrap trust from the network.
var injectedCLIReleasePublicKeyB64 string
var injectedCLIReleaseKeyID string
var injectedCLIReleaseRepoURL string

func cliInstallSVN(program string) (string, error) {
	if program == "" {
		program = "svn"
	}
	resolved, err := exec.LookPath(program)
	if err != nil {
		return "", fmt.Errorf("Subversion is required for CLI installation: %w", err)
	}
	return filepath.Abs(resolved)
}

type cliUpdater struct {
	resolver        releaseenvelope.Resolver
	fetcher         releaseenvelope.Fetcher
	prefix          string
	platform        string
	minimumSequence uint64
	out             io.Writer
	// The production caller applies the OpenBSD profile after creating the
	// exact install/state directories, before reading release material.
	sandbox func([]string, string) error
}

func cmdInstall(args []string) int {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	prefix := flags.String("prefix", "/usr/local", "absolute CLI installation prefix")
	check := flags.Bool("check", false, "verify the CLI channel without downloading or installing its archive")
	timeout := flags.Duration("timeout", 30*time.Minute, "maximum installation time")
	svn := flags.String("svn", "", "Subversion executable (default: look up svn)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "filees-client install: unexpected arguments or invalid timeout")
		return 2
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	if platform != "linux-amd64" && platform != "openbsd-amd64" {
		fmt.Fprintln(os.Stderr, "filees-client install: supported on Linux/amd64 and OpenBSD/amd64")
		return 1
	}
	key, err := cliReleaseKey()
	if err != nil {
		fmt.Fprintln(os.Stderr, "filees-client install:", err)
		return 1
	}
	program, err := cliInstallSVN(*svn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "filees-client install:", err)
		return 1
	}
	fetcher := svnfetch.SVN{Program: program, RepoURL: injectedCLIReleaseRepoURL}
	updater := cliUpdater{
		resolver: releaseenvelope.Resolver{Fetcher: fetcher,
			Verifier:    releaseenvelope.Ed25519Verifier{Keys: map[string][]byte{injectedCLIReleaseKeyID: key}},
			TrustedKeys: []string{injectedCLIReleaseKeyID}},
		fetcher: fetcher, prefix: *prefix, platform: platform, out: os.Stdout,
		sandbox: func(paths []string, state string) error { return lockInstallSandbox(paths, state, program) },
	}
	if strings.HasPrefix(cliSourceRevision, "r") {
		updater.minimumSequence, err = strconv.ParseUint(strings.TrimPrefix(cliSourceRevision, "r"), 10, 64)
		if err != nil || updater.minimumSequence == 0 {
			fmt.Fprintln(os.Stderr, "filees-client install: invalid compiled source revision")
			return 1
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := updater.run(ctx, *check); err != nil {
		fmt.Fprintln(os.Stderr, "filees-client install:", err)
		return 1
	}
	return 0
}

func cliReleaseKey() ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(injectedCLIReleasePublicKeyB64)
	if err != nil || len(key) == 0 || injectedCLIReleaseKeyID == "" {
		return nil, errors.New("this build has no trusted CLI release key; use a signed distribution build")
	}
	key, err = releaseenvelope.CanonicalSignifyPublicKey(key)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(injectedCLIReleaseRepoURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "svn" && u.Scheme != "https") {
		return nil, errors.New("invalid compiled CLI release repository (svn or https required)")
	}
	return key, nil
}

func (u cliUpdater) run(ctx context.Context, check bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if u.platform != "linux-amd64" && u.platform != "openbsd-amd64" {
		return errors.New("unsupported CLI platform")
	}
	if !filepath.IsAbs(u.prefix) || filepath.Clean(u.prefix) != u.prefix || filepath.Dir(u.prefix) == u.prefix {
		return errors.New("installation prefix must be an absolute, clean, non-root directory")
	}
	if u.out == nil {
		u.out = io.Discard
	}
	manDir := filepath.Join(u.prefix, "share", "man", "man1")
	if u.platform == "openbsd-amd64" {
		manDir = filepath.Join(u.prefix, "man", "man1")
	}
	stateDir := filepath.Join(u.prefix, "share", "filees-client-update")
	dirs := []string{filepath.Join(u.prefix, "bin"), filepath.Join(u.prefix, "share", "doc", "filees-client"), manDir, stateDir}
	if err := installDirectory(stateDir, 0o700); err != nil {
		return err
	}
	if info, err := os.Stat(stateDir); err != nil {
		return err
	} else if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return errors.New("CLI update state directory must be private (mode 0700)")
	}
	if !check {
		for _, dir := range dirs[:3] {
			if err := installDirectory(dir, 0o755); err != nil {
				return err
			}
		}
	}
	lockPath := filepath.Join(stateDir, "install.lock")
	if err := regularInstallTarget(lockPath); err != nil {
		return err
	}
	lock, err := acquireFileLock(lockPath)
	if err != nil {
		return fmt.Errorf("another CLI installation is active or unavailable: %w", err)
	}
	defer lock.Close()
	if u.sandbox != nil {
		if err := u.sandbox(dirs, stateDir); err != nil {
			return err
		}
	}
	store := clientupdate.StateStore{Path: filepath.Join(stateDir, "state.json")}
	if err := regularInstallTarget(store.Path); err != nil {
		return err
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	fmt.Fprintln(u.out, "Verifying the signed CLI channel...")
	resolved, err := u.resolver.Resolve(ctx, "channels/cli.json", "cli", u.platform)
	if err != nil {
		return err
	}
	if err := state.Check(resolved.Envelope); err != nil {
		return err
	}
	if resolved.Envelope.Sequence < u.minimumSequence {
		return fmt.Errorf("CLI channel rollback: sequence %d is older than this installer (%d)", resolved.Envelope.Sequence, u.minimumSequence)
	}
	artifacts := resolved.Manifest.Artifacts
	if len(artifacts) != 1 || artifacts[0].Kind != "bundle" || artifacts[0].Source != "filees-client-"+u.platform+".tar.gz" {
		return errors.New("CLI manifest must name exactly its platform's standalone archive")
	}
	if check {
		fmt.Fprintf(u.out, "Verified CLI %s (%s, %s); no payload installed.\n", resolved.Manifest.Version, resolved.Envelope.ReleaseID, u.platform)
		return nil
	}
	fmt.Fprintln(u.out, "Downloading and checking the CLI archive...")
	staged, err := (clientupdate.BundleStager{Fetcher: u.fetcher, Root: stateDir, MaxFiles: 16, MaxBundleSize: 128 << 20}).Stage(ctx, resolved)
	if err != nil {
		return err
	}
	defer staged.Remove()
	files, err := cliPayload(staged.Root, u.platform, dirs)
	if err != nil {
		return err
	}
	// Persist the anti-rollback watermark BEFORE the first replacement. A
	// failure or process death can retry this exact release, but never fall
	// back to older signed code. InstalledVersion advances only on success.
	watermark, err := state.Advance(resolved.Envelope, state.InstalledVersion)
	if err != nil {
		return err
	}
	if err := store.Save(watermark); err != nil {
		return err
	}
	// Documentation goes first. Each file is prepared next to its target and
	// made durable and executable before rename; the running binary stays
	// intact until the final promotion. Retrying repairs any partial install.
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := os.ReadFile(file.source)
		if err != nil {
			return err
		}
		if file.mode == 0o755 {
			old, err := os.ReadFile(file.target)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err == nil && !bytes.Equal(old, data) {
				if err := atomicInstallFile(filepath.Join(stateDir, "previous-filees-client"), old, 0o700); err != nil {
					return fmt.Errorf("preserve previous CLI: %w", err)
				}
			}
		}
		if err := atomicInstallFile(file.target, data, file.mode); err != nil {
			return err
		}
	}
	installed, err := watermark.Advance(resolved.Envelope, resolved.Manifest.Version)
	if err != nil {
		return err
	}
	if err := store.Save(installed); err != nil {
		return err
	}
	fmt.Fprintf(u.out, "Installed CLI %s (%s) at %s.\n", resolved.Manifest.Version, resolved.Envelope.ReleaseID, files[2].target)
	return nil
}

type cliInstallFile struct {
	source, target string
	mode           os.FileMode
}

func cliPayload(root, platform string, dirs []string) ([]cliInstallFile, error) {
	files := []cliInstallFile{
		{filepath.Join(root, "share", "doc", "filees-client", "README.md"), filepath.Join(dirs[1], "README.md"), 0o644},
		{filepath.Join(root, "share", "man", "man1", "filees-client.1"), filepath.Join(dirs[2], "filees-client.1"), 0o644},
		{filepath.Join(root, "bin", "filees-client"), filepath.Join(dirs[0], "filees-client"), 0o755},
	}
	wanted := make(map[string]bool)
	for _, file := range files {
		wanted[file.source] = true
		if err := regularInstallTarget(file.target); err != nil {
			return nil, err
		}
	}
	if err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() || !wanted[name] {
			return fmt.Errorf("unexpected CLI archive file %s", name)
		}
		delete(wanted, name)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(wanted) != 0 {
		return nil, errors.New("CLI archive is missing its binary, README or manual")
	}
	// The signed platform binding is authoritative; additionally reject a
	// non-ELF or wrong-architecture executable before replacing the old one.
	binaryData, err := os.ReadFile(files[2].source)
	if err != nil {
		return nil, err
	}
	if len(binaryData) < 20 || !bytes.Equal(binaryData[:4], []byte{0x7f, 'E', 'L', 'F'}) ||
		binaryData[4] != 2 || binaryData[5] != 1 || binary.LittleEndian.Uint16(binaryData[18:20]) != 62 {
		return nil, fmt.Errorf("CLI archive does not contain an ELF amd64 binary for %s", platform)
	}
	return files, nil
}

func installDirectory(name string, mode os.FileMode) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(name)
		if parent == name {
			return err
		}
		if err := installDirectory(parent, 0o755); err != nil {
			return err
		}
		return os.Mkdir(name, mode)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("install directory is not a real directory: %s", name)
	}
	// Check every ancestor as well: a symlinked bin/share must not redirect
	// root's replacement into an unrelated tree.
	if parent := filepath.Dir(name); parent != name {
		return installDirectory(parent, 0o755)
	}
	return nil
}

func regularInstallTarget(name string) error {
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular installation target: %s", name)
	}
	return nil
}

func atomicInstallFile(target string, data []byte, mode os.FileMode) error {
	if err := regularInstallTarget(target); err != nil {
		return err
	}
	if old, err := os.ReadFile(target); err == nil && sha256.Sum256(old) == sha256.Sum256(data) {
		info, err := os.Stat(target)
		if err == nil && info.Mode().Perm() == mode {
			return nil
		}
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".filees-client-install-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(target))
}
