//go:build !windows

package servertool

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/internal/obsandbox"
	"filees/internal/uploadworker"
	"filees/pkg/avscan"
	"filees/pkg/realmbranding"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"filees/public-shares/channel"
	"filees/public-shares/intake"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

type uploadSandboxAuthority struct{ owner, repo string }

func (a uploadSandboxAuthority) OwnsActiveRepository(owner, repo string) error {
	if owner != a.owner || repo != a.repo {
		return errors.New("not owner")
	}
	return nil
}
func (a uploadSandboxAuthority) ActiveRealmAlias(owner string) (string, error) {
	if owner != a.owner {
		return "", errors.New("not owner")
	}
	return "sandbox-test", nil
}
func (uploadSandboxAuthority) ActiveRealmBranding(string) (realmbranding.Branding, error) {
	return realmbranding.Default(), nil
}

func TestUploadMaintenanceProfilesSeparateSeedFromReaper(t *testing.T) {
	var config serverconfig.Config
	config.Repositories.Root = "/srv/repos"
	config.Activation.Root = "/srv/activation"
	config.Repositories.ResultsRoot = "/srv/results"
	config.Repositories.SVNAdminBinary = "/usr/local/bin/svnadmin"
	config.Upload.IntakeRoot = "/srv/intake"
	config.Upload.TrashRoot = "/srv/quarantine"
	config.Upload.AVCommand = []string{"/usr/local/bin/clamdscan", "--stream"}
	for _, seed := range []bool{false, true} {
		profile := uploadMaintenanceProfile(config, seed)
		if err := obsandbox.Validate(profile); err != nil {
			t.Fatal(err)
		}
		certificates := false
		for _, path := range profile.Paths {
			if path.Name == "/etc/clamav-certs" {
				certificates = true
				if seed || path.Perms != "r" {
					t.Fatal("scanner certificates must only be readable by the reaper", path)
				}
			}
			if path.Name == "/" || path.Name == config.Activation.ServiceWorkingCopy {
				t.Fatal("overbroad path", path)
			}
		}
		if !seed && !certificates {
			t.Fatal("scanner signature certificates unavailable")
		}
		if seed && (len(profile.Paths) != 2 || strings.Contains(profile.Promises, "exec") || strings.Contains(profile.Promises, "inet")) {
			t.Fatal(profile)
		}
		if !seed && !strings.Contains(profile.Promises, "unix") {
			t.Fatal("clamd socket unavailable")
		}
	}
}

func TestUploadCronEntrypointsFailClosedBeforeConfigOnSandboxError(t *testing.T) {
	old := sandboxBegin
	defer func() { sandboxBegin = old }()
	sandboxBegin = func(string) error { return errors.New("test sandbox refused") }
	var out, stderr bytes.Buffer
	if code := RunUploadReap([]string{"-config", "/missing"}, nil, &out, &stderr); code != ExitSoftware {
		t.Fatal(code, stderr.String())
	}
	if code := RunUploadSeedReject([]string{"-config", "/missing", "-alias", "test"}, nil, &out, &stderr); code != ExitSoftware {
		t.Fatal(code, stderr.String())
	}
}

func TestUploadMaintenanceNativeConfinement(t *testing.T) {
	if runtime.GOOS != "openbsd" {
		t.Skip("requires pledge/unveil")
	}
	if isolateSandboxingTest(t, "TestUploadMaintenanceNativeConfinement") {
		return
	}
	root := sandboxedTestRoot(t)
	var config serverconfig.Config
	config.Activation.Root = filepath.Join(root, "activation")
	config.Repositories.Root = filepath.Join(root, "repos")
	config.Repositories.SVNAdminBinary = "/usr/local/bin/svnadmin"
	config.Repositories.ResultsRoot = filepath.Join(root, "results")
	config.Upload.IntakeRoot = filepath.Join(root, "intake")
	config.Upload.TrashRoot = filepath.Join(root, "trash")
	config.Upload.AVCommand = []string{"/usr/bin/true"}
	for _, dir := range []string{config.Activation.Root, config.Repositories.Root, config.Repositories.ResultsRoot, config.Upload.IntakeRoot, config.Upload.TrashRoot, uploadAcceptedRoot(config), config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	stateRoot := config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)
	protected := filepath.Join(stateRoot, "channel-record.json")
	if err := os.WriteFile(protected, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	owner, parent, uploadRepo := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := channel.Store{Root: stateRoot, TokenKey: []byte(strings.Repeat("t", 32)), Authority: uploadSandboxAuthority{owner, parent}}
	upload, _, err := store.CreateUpload(uuid.NewString(), owner, manifest.Upload{
		OwnerRealm: owner, AuthorityRepoID: parent, UploadRepoID: uploadRepo,
		Slug: "arrival", Recipients: []string{"test@example.com"}, CollisionPolicy: manifest.CollisionDeny,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(config.Repositories.Root, uploadRepo)
	if out, err := exec.Command(config.Repositories.SVNAdminBinary, "create", repo).CombinedOutput(); err != nil {
		t.Fatalf("create delivery repository: %v %s", err, out)
	}
	spool := intake.Store{Root: config.Upload.IntakeRoot, MaxBytes: 1024}
	job, err := spool.Accept(upload.ChannelID, upload.Alias, upload.Slug, strings.Repeat("ab", 32), "received.pdf", strings.NewReader("received payload"))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := "/etc/passwd"
	if _, err := os.Stat(forbidden); err != nil {
		t.Fatal(err)
	}
	if err := sandboxBegin(uploadMaintenancePromises); err != nil {
		t.Fatal(err)
	}
	if err := sandboxApplyForExec(uploadMaintenanceProfile(config, false), uploadMaintenancePromises+" prot_exec unveil"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(forbidden); err == nil {
		t.Fatal("outside file visible")
	}
	if err := os.WriteFile(filepath.Join(config.Upload.TrashRoot, "probe"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := (uploadworker.Reaper{TrashRoot: config.Upload.TrashRoot}).PurgeExpired(context.Background(), time.Now()); err != nil {
		t.Fatalf("trash lock/sweep under sandbox: %v", err)
	}
	// A real arrival record, including creation of its initially absent
	// directory and atomic rename/fsync, must survive the cron sandbox.
	entry := channel.Accepted{ChannelID: "test-channel", UploadID: "test-upload", RepoPath: "received.pdf", Revision: 1}
	if err := store.RecordAccepted(entry); err != nil {
		t.Fatalf("arrival index under sandbox: %v", err)
	}
	if err := store.RecordAccepted(entry); err != nil {
		t.Fatalf("replacement of arrival index under sandbox: %v", err)
	}
	items, err := store.ListAccepted(entry.ChannelID)
	if err != nil || len(items) != 1 || items[0].RepoPath != entry.RepoPath {
		t.Fatalf("arrival listing under sandbox: %+v, %v", items, err)
	}
	if err := os.WriteFile(protected, []byte("changed"), 0600); err == nil {
		t.Fatal("reaper can overwrite channel configuration")
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "unexpected.json"), nil, 0600); err == nil {
		t.Fatal("reaper can create unrelated channel configuration")
	}
	reaper := uploadworker.Reaper{Intake: spool, Channels: &store, ReposRoot: config.Repositories.Root, TrashRoot: config.Upload.TrashRoot,
		Scanner: avscan.Static{Verdict: avscan.Clean}, Publisher: uploadworker.Publisher{
			SVNMucc: config.Repositories.EffectiveSVNMuccBinary(), SVNLook: config.Repositories.EffectiveSVNLookBinary(),
		}}
	summary, err := reaper.Reap(context.Background())
	if err != nil || summary.Accepted != 1 || summary.Unindexed != 0 || summary.Failed != 0 {
		t.Fatalf("native commit and index: %+v, %v", summary, err)
	}
	listed, err := (repoworker.ChannelUploadService{Channels: &store}).ListShelf(context.Background(), owner, upload.ChannelID)
	if err != nil || len(listed.Items) != 1 || listed.Items[0].UploadID != job.UploadID || listed.Items[0].Revision != 1 || listed.Items[0].SHA256 != job.SHA256 {
		t.Fatalf("shelf after native commit: %+v, %v", listed, err)
	}
	if out, err := exec.Command(config.Repositories.EffectiveSVNLookBinary(), "cat", repo, "received.pdf").CombinedOutput(); err != nil || string(out) != "received payload" {
		t.Fatalf("committed payload: %v %s", err, out)
	}
	if summary, err := reaper.Reap(context.Background()); err != nil || summary.Accepted != 0 {
		t.Fatalf("completed upload was retried: %+v, %v", summary, err)
	}
	for _, tool := range []string{config.Repositories.EffectiveSVNMuccBinary(), config.Repositories.EffectiveSVNLookBinary(), config.Upload.AVCommand[0]} {
		if out, err := exec.Command(tool, "--version").CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", tool, err, out)
		}
	}
}

// The opt-in database contains a laboratory signature for FILEES-LAB-REJECT.
// Exercise the actual scanner after exec, rather than a Static AV verdict.
func TestUploadScannerNativeConfinement(t *testing.T) {
	if runtime.GOOS != "openbsd" {
		t.Skip("requires pledge/unveil and an isolated ClamAV installation")
	}
	database := os.Getenv("FILEES_TEST_CLAMAV_DATABASE")
	if database == "" {
		t.Skip("requires the isolated laboratory ClamAV database")
	}
	if filepath.Dir(database) != "/var/db/clamav" || filepath.Clean(database) != database {
		t.Fatal("laboratory database must be a file in the scanner database directory")
	}
	if isolateSandboxingTest(t, "TestUploadScannerNativeConfinement") {
		return
	}
	scannerPath, err := exec.LookPath("clamscan")
	if err != nil {
		t.Fatal(err)
	}
	root := sandboxedTestRoot(t)
	var config serverconfig.Config
	config.Activation.Root = filepath.Join(root, "activation")
	config.Repositories.Root = filepath.Join(root, "repos")
	config.Repositories.ResultsRoot = filepath.Join(root, "results")
	config.Repositories.SVNAdminBinary = "/usr/local/bin/svnadmin"
	config.Upload.IntakeRoot = filepath.Join(root, "intake")
	config.Upload.TrashRoot = filepath.Join(root, "trash")
	config.Upload.AVCommand = []string{scannerPath, "--database=" + database, "--no-summary", "--stdout"}
	for _, dir := range []string{config.Activation.Root, config.Repositories.Root, config.Repositories.ResultsRoot, config.Upload.IntakeRoot, config.Upload.TrashRoot, uploadAcceptedRoot(config), config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	scanner := avscan.Command{Path: scannerPath, Args: config.Upload.AVCommand[1:]}
	clean := filepath.Join(config.Upload.IntakeRoot, "clean")
	infected := filepath.Join(config.Upload.IntakeRoot, "scanner-test")
	for path, payload := range map[string]string{clean: "FILEES-LAB-CLEAN\n", infected: "FILEES-LAB-REJECT\n"} {
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
	}
	assertScan := func() {
		t.Helper()
		for path, want := range map[string]avscan.Verdict{clean: avscan.Clean, infected: avscan.Infected} {
			verdict, detail, err := scanner.Scan(context.Background(), path)
			if err != nil || verdict != want {
				t.Fatalf("real scanner: verdict=%v want=%v error=%v detail=%s", verdict, want, err, detail)
			}
		}
	}
	assertScan() // Do not mistake a broken database for a confinement failure.
	if err := sandboxBegin(uploadMaintenancePromises); err != nil {
		t.Fatal(err)
	}
	if err := sandboxApplyForExec(uploadMaintenanceProfile(config, false), uploadMaintenancePromises+" prot_exec unveil"); err != nil {
		t.Fatal(err)
	}
	assertScan()
	if _, err := os.ReadFile("/etc/passwd"); err == nil {
		t.Fatal("unrelated configuration became visible")
	}
	if err := os.WriteFile("/etc/clamav-certs/filees-test-forbidden", nil, 0600); err == nil {
		t.Fatal("scanner trust directory became writable")
	}
}
