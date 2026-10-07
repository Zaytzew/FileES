package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/internal/durable"
	"filees/pkg/client"
	"filees/pkg/clientprofile"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/provisioning"

	"github.com/google/uuid"
)

const dumpJobSchema = "filees.cli-dump-load/v1"

type dumpFlags struct {
	profile, source, target, state, name, svn, ssh string
	timeout                                        time.Duration
}

type dumpJob struct {
	Schema      string `json:"schema"`
	OperationID string `json:"operation_id"`
	Profile     string `json:"profile"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	Name        string `json:"name"`
	SHA256      string `json:"sha256,omitempty"`
	RepoURL     string `json:"repo_url,omitempty"`
	NewUUID     string `json:"new_uuid,omitempty"`
	Complete    bool   `json:"complete,omitempty"`
}

func cmdDumpLoad(args []string) int {
	flags := flag.NewFlagSet("dump-load", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var opt dumpFlags
	flags.StringVar(&opt.profile, "profile", "", "absolute activated client-profile.json")
	flags.StringVar(&opt.source, "dump", "", "absolute SVN dump file")
	flags.StringVar(&opt.target, "target", "", "absolute path of the new working copy")
	flags.StringVar(&opt.state, "state", "", "absolute private directory for this resumable import")
	flags.StringVar(&opt.name, "name", "", "new repository name")
	flags.StringVar(&opt.svn, "svn", defaultSVN, "absolute svn executable")
	flags.StringVar(&opt.ssh, "ssh", defaultSSH, "absolute ssh executable")
	flags.DurationVar(&opt.timeout, "timeout", 3*time.Hour, "whole import timeout")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if err := validateDumpFlags(opt); err != nil {
		fmt.Fprintln(os.Stderr, "dump-load:", err)
		return 2
	}
	profile, err := clientprofile.Load(opt.profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dump-load profile:", err)
		return 1
	}
	if err := os.Mkdir(opt.state, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		fmt.Fprintln(os.Stderr, "dump-load state:", err)
		return 1
	}
	if info, err := os.Lstat(opt.state); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintln(os.Stderr, "dump-load state: state must be a real directory")
		return 1
	}
	lock, err := acquireFileLock(filepath.Join(opt.state, "run.lock"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "dump-load:", err)
		return 1
	}
	defer lock.Close()
	job, err := loadOrCreateDumpJob(opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dump-load state:", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Join(opt.state, "carrier"), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "dump-load carrier:", err)
		return 1
	}
	if err := lockDumpSandbox(opt, profile); err != nil {
		fmt.Fprintln(os.Stderr, "dump-load sandbox:", err)
		return 1
	}
	cfg := Config{SVN: opt.svn, SSH: opt.ssh, Identity: profile.IdentityFile,
		KnownHosts: profile.KnownHosts, Host: profile.Address, Port: profile.SSHPort,
		LogDir: opt.state, Copies: map[string]workingCopySpec{"carrier": {
			ID: "carrier", Path: filepath.Join(opt.state, "carrier"), URL: profile.ServiceURL,
		}}}
	if err := prepareSSHPath(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "dump-load tools:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), opt.timeout)
	defer cancel()
	transport, err := controlclient.New(controlclient.Config{
		Address: profile.Address, Port: profile.SSHPort,
		IdentityFile: profile.IdentityFile, KnownHosts: profile.KnownHosts,
		Timeout: opt.timeout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dump-load transport:", err)
		return 1
	}
	if err := runDumpLoad(ctx, opt, profile, cfg.svnClient(opt.timeout), transport, &job); err != nil {
		fmt.Fprintln(os.Stderr, "dump-load:", err)
		return 1
	}
	return 0
}

func validateDumpFlags(opt dumpFlags) error {
	for _, path := range []string{opt.profile, opt.source, opt.target, opt.state, opt.svn, opt.ssh} {
		if !filepath.IsAbs(path) {
			return errors.New("profile, dump, target, state, svn and ssh must be absolute")
		}
	}
	if strings.TrimSpace(opt.name) == "" || opt.timeout <= 0 {
		return errors.New("name and positive timeout are required")
	}
	if filepath.Clean(opt.target) == filepath.Clean(opt.state) || strings.HasPrefix(filepath.Clean(opt.target)+string(filepath.Separator), filepath.Clean(opt.state)+string(filepath.Separator)) {
		return errors.New("target and state must be separate")
	}
	if _, err := os.Stat(filepath.Dir(opt.target)); err != nil {
		return fmt.Errorf("target parent: %w", err)
	}
	return nil
}

func loadOrCreateDumpJob(opt dumpFlags) (dumpJob, error) {
	path := filepath.Join(opt.state, "job.json")
	raw, err := os.ReadFile(path)
	if err == nil {
		var job dumpJob
		if err := json.Unmarshal(raw, &job); err != nil {
			return dumpJob{}, err
		}
		if job.Schema != dumpJobSchema || job.Profile != opt.profile || job.Source != opt.source || job.Target != opt.target || job.Name != opt.name {
			return dumpJob{}, errors.New("existing state belongs to a different import")
		}
		if _, err := uuid.Parse(job.OperationID); err != nil {
			return dumpJob{}, errors.New("stored operation ID is invalid")
		}
		return job, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return dumpJob{}, err
	}
	entries, err := os.ReadDir(opt.state)
	if err != nil {
		return dumpJob{}, err
	}
	for _, entry := range entries {
		if entry.Name() != "run.lock" {
			return dumpJob{}, errors.New("state directory is not empty and has no job")
		}
	}
	if _, err := os.Lstat(opt.target); err == nil {
		return dumpJob{}, errors.New("new working-copy target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return dumpJob{}, err
	}
	job := dumpJob{Schema: dumpJobSchema, OperationID: uuid.NewString(),
		Profile: opt.profile, Source: opt.source, Target: opt.target, Name: opt.name}
	return job, saveDumpJob(opt.state, job)
}

func saveDumpJob(root string, job dumpJob) error {
	raw, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".job-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(root, "job.json")); err != nil {
		return err
	}
	return durable.SyncDirectory(root)
}

func prepareDumpCarrier(opt dumpFlags, job *dumpJob) (int64, error) {
	carrier := filepath.Join(opt.state, "carrier")
	file := filepath.Join(carrier, "dump.svndump")
	if job.SHA256 != "" {
		f, err := os.Open(file)
		if err != nil {
			return 0, fmt.Errorf("stored carrier missing: %w", err)
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return 0, errors.New("stored carrier is not a regular file")
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, f); err != nil || hex.EncodeToString(hash.Sum(nil)) != job.SHA256 {
			return 0, errors.New("stored carrier digest changed")
		}
		return info.Size(), nil
	}
	if err := os.Mkdir(carrier, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return 0, err
	}
	if _, err := os.Lstat(filepath.Join(carrier, ".svn")); err == nil {
		return 0, errors.New("carrier WC exists without a stored source digest")
	}
	sourceInfo, err := os.Lstat(opt.source)
	if err != nil || !sourceInfo.Mode().IsRegular() {
		return 0, errors.New("dump must be a regular file, not a symlink")
	}
	source, err := os.Open(opt.source)
	if err != nil {
		return 0, err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 28 {
		return 0, errors.New("dump must be a nonempty regular file")
	}
	header := make([]byte, 28)
	if _, err := io.ReadFull(source, header); err != nil || !strings.HasPrefix(string(header), "SVN-fs-dump-format-version:") {
		return 0, errors.New("dump header is invalid")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	dest, err := os.OpenFile(file, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(dest, hash), source)
	if copyErr == nil {
		copyErr = dest.Sync()
	}
	closeErr := dest.Close()
	if copyErr != nil || closeErr != nil || n != info.Size() {
		os.Remove(file)
		return 0, errors.New("dump carrier copy incomplete")
	}
	job.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if err := saveDumpJob(opt.state, *job); err != nil {
		return 0, err
	}
	return n, nil
}

func runDumpLoad(ctx context.Context, opt dumpFlags, profile clientprofile.Profile, svn client.Client, transport provisioning.ControlExchange, job *dumpJob) error {
	if job.Complete {
		if err := verifyDumpCheckout(ctx, svn, opt.target, job.RepoURL, job.NewUUID); err != nil {
			return fmt.Errorf("completed import target changed: %w", err)
		}
		fmt.Printf("repo_url=%s wc=%s\n", job.RepoURL, opt.target)
		return nil
	}
	store, err := provisioning.NewStore(filepath.Join(opt.state, "provisioning"))
	if err != nil {
		return err
	}
	op, err := store.Get(job.OperationID)
	if errors.Is(err, os.ErrNotExist) {
		var size int64
		size, err = prepareDumpCarrier(opt, job)
		if err != nil {
			return err
		}
		op, err = store.CreateValidatedSnapshot(job.OperationID, profile.ClientID,
			filepath.Join(opt.state, "carrier"), opt.name, size, 1)
	}
	if err != nil {
		return err
	}
	if op.ClientID != profile.ClientID || op.LocalPath != filepath.Join(opt.state, "carrier") || op.Name != opt.name {
		return errors.New("stored provisioning operation does not match this import")
	}
	if op.State != provisioning.StateActive {
		// A single dump file must fit one batch, leaving exactly one carrier
		// commit; the server refuses LOAD on a multi-commit repository.
		size := op.SnapshotBytes
		if size < 1 {
			return errors.New("stored dump size is invalid")
		}
		op, err = (provisioning.Orchestrator{Store: store, Control: transport, SVN: svn,
			Limits: provisioning.ImportLimits{MaxBatchFiles: 1, MaxBatchBytes: size},
		}).RunCreate(ctx, job.OperationID)
		if err != nil {
			return err
		}
	}
	if op.State != provisioning.StateActive || op.RepoID == "" || op.RepoURL == "" || op.Revision != 1 {
		return fmt.Errorf("carrier repository is not a single r1 commit (state=%s revision=%d)", op.State, op.Revision)
	}
	if job.RepoURL != "" && job.RepoURL != op.RepoURL {
		return errors.New("stored repository URL changed")
	}
	job.RepoURL = op.RepoURL
	if err := saveDumpJob(opt.state, *job); err != nil {
		return err
	}
	loadID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(job.OperationID+":load"))
	requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(job.OperationID+":load-request"))
	ticket, err := control.NewTicket(loadID.String(), requestID.String(), control.TicketLoadRepositoryDump,
		profile.ClientID, control.LoadRepositoryDumpPayload{RepoID: op.RepoID}, time.Now())
	if err != nil {
		return err
	}
	result, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return fmt.Errorf("LOAD_REPOSITORY_DUMP response uncertain; rerun with the same --state: %w", err)
	}
	if result.Status != control.ResultOK {
		if result.Error != nil {
			return fmt.Errorf("LOAD_REPOSITORY_DUMP: %s: %s", result.Error.Code, result.Error.Message)
		}
		return errors.New("LOAD_REPOSITORY_DUMP rejected without error body")
	}
	var loaded control.LoadRepositoryDumpResult
	if err := control.DecodeResultPayload(result.Result, &loaded); err != nil {
		return err
	}
	if loaded.RepoID != op.RepoID || loaded.NewUUID == "" {
		return errors.New("LOAD_REPOSITORY_DUMP result does not match repository")
	}
	job.NewUUID = loaded.NewUUID
	if err := saveDumpJob(opt.state, *job); err != nil {
		return err
	}
	if err := materializeDumpCheckout(ctx, svn, opt.target, job); err != nil {
		return err
	}
	job.Complete = true
	if err := saveDumpJob(opt.state, *job); err != nil {
		return err
	}
	// The old WC is a carrier for the previous generation and cannot be
	// updated after the atomic repository replacement. It is private scratch.
	if err := os.RemoveAll(filepath.Join(opt.state, "carrier")); err != nil {
		fmt.Fprintln(os.Stderr, "dump-load: carrier cleanup deferred:", err)
	}
	fmt.Printf("repo_id=%s repo_url=%s wc=%s\n", op.RepoID, op.RepoURL, opt.target)
	return nil
}

func materializeDumpCheckout(ctx context.Context, svn client.Client, target string, job *dumpJob) error {
	if _, err := os.Lstat(target); err == nil {
		return verifyDumpCheckout(ctx, svn, target, job.RepoURL, job.NewUUID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage := target + ".filees-new-" + job.OperationID
	if _, err := os.Lstat(stage); err == nil {
		if err := verifyDumpCheckout(ctx, svn, stage, job.RepoURL, job.NewUUID); err != nil {
			return fmt.Errorf("existing checkout stage needs review: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		if _, err := svn.Checkout(ctx, job.RepoURL, stage); err != nil {
			return fmt.Errorf("checkout imported repository: %w", err)
		}
	}
	if err := verifyDumpCheckout(ctx, svn, stage, job.RepoURL, job.NewUUID); err != nil {
		return err
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return errors.New("target appeared during import; staged WC preserved")
	}
	if err := os.Rename(stage, target); err != nil {
		return err
	}
	return durable.SyncDirectory(filepath.Dir(target))
}

func verifyDumpCheckout(ctx context.Context, svn client.Client, path, repoURL, repoUUID string) error {
	info, err := svn.GetInfo(ctx, path)
	if err != nil {
		return err
	}
	// The native Windows svn CLI emits CRLF, while Unix emits LF.
	info = strings.ReplaceAll(info, "\r\n", "\n")
	if !strings.Contains(info, "URL: "+repoURL+"\n") || !strings.Contains(info, "Repository UUID: "+repoUUID+"\n") {
		return errors.New("working copy URL or UUID differs from imported generation")
	}
	entries, err := svn.Status(ctx, path, nil)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Conflicted || entry.Props == "conflicted" || (entry.Item != "normal" && entry.Item != "none" && entry.Item != "external") {
			return fmt.Errorf("working copy is not clean at %s (%s)", entry.Path, entry.Item)
		}
	}
	return nil
}
