package activation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientview"
	"filees/pkg/onboarding"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

func TestActivationStagesProofAndPublishesOneServiceRevision(t *testing.T) {
	manager, config := newActivationTestManager(t)
	grant := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	keys, _ := os.ReadFile(config.AuthorizedKeysFile)
	if !strings.Contains(string(keys), `restrict,command="`+config.ClientEntryPath+" "+grant.OperationID+" "+grant.ClientID+`"`) || !strings.Contains(string(keys), "ssh-ed25519") {
		t.Fatalf("staged authorized_keys=%s", keys)
	}
	if _, err := manager.Publish(context.Background(), grant); err == nil {
		t.Fatal("activation published without server proof receipt")
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatal(err)
	}
	revision, err := manager.Publish(context.Background(), grant)
	if err != nil || revision <= 1 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
	view, err := clientview.Load(filepath.Join(config.ServiceWorkingCopy, "clients", grant.ClientID, "view.json"))
	if err != nil || !view.CanCreateRepositories() || view.Capabilities == nil {
		t.Fatalf("activation view capabilities=%+v err=%v", view.Capabilities, err)
	}
	again, err := manager.Publish(context.Background(), grant)
	if err != nil || again != revision {
		t.Fatalf("idempotent publish=%d err=%v, want %d", again, err, revision)
	}
	second := testActivationGrant(t, time.Now().Add(time.Hour))
	second.RealmID = grant.RealmID
	if err := manager.Stage(second); err != nil {
		t.Fatal(err)
	}
	if err := manager.RecordProof(second.OperationID, second.ClientID); err != nil {
		t.Fatal(err)
	}
	secondRevision, err := manager.Publish(context.Background(), second)
	if err != nil || secondRevision <= revision {
		t.Fatalf("second client in realm revision=%d err=%v, first=%d", secondRevision, err, revision)
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err != nil {
		t.Fatalf("active client was denied subsequent SVN entry: %v", err)
	}
	revokeRevision, err := manager.Revoke(context.Background(), grant.ClientID, "administrator requested redeploy")
	if err != nil || revokeRevision <= revision {
		t.Fatalf("revoke revision=%d err=%v", revokeRevision, err)
	}
	if again, err := manager.Revoke(context.Background(), grant.ClientID, "administrator requested redeploy"); err != nil || again != revokeRevision {
		t.Fatalf("idempotent revoke revision=%d err=%v", again, err)
	}
	if _, err := manager.Revoke(context.Background(), grant.ClientID, "different reason"); err == nil {
		t.Fatal("conflicting revoke reason was accepted")
	}
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err == nil {
		t.Fatal("revoked key retained SVN entry")
	}
	keys, _ = os.ReadFile(config.AuthorizedKeysFile)
	if strings.Contains(string(keys), grant.ClientID) {
		t.Fatalf("revoked client remains in authorized_keys: %s", keys)
	}
	for _, path := range []string{
		filepath.Join(config.ServiceWorkingCopy, "admin", "clients", grant.ClientID+".json"),
		filepath.Join(config.ServiceWorkingCopy, "clients", grant.ClientID, "view.json"),
		filepath.Join(config.ServiceWorkingCopy, "admin", "audit", grant.OperationID+".json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("published path %s: %v", path, err)
		}
	}
	authz, _ := os.ReadFile(config.AuthzFile)
	if strings.Contains(string(authz), "[/clients/"+grant.ClientID+"]") || !strings.Contains(string(authz), "[/clients/"+second.ClientID+"]") {
		t.Fatalf("revoked/active authz=%s", authz)
	}
}

func TestExpiredStagingIsFailClosedAndRemovedFromRuntimeAccess(t *testing.T) {
	manager, config := newActivationTestManager(t)
	now := time.Now().UTC()
	manager.now = func() time.Time { return now }
	grant := testActivationGrant(t, now.Add(time.Minute))
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := manager.RecordProof(grant.OperationID, grant.ClientID); err == nil {
		t.Fatal("expired staged key produced a proof receipt")
	}
	count, err := manager.CleanupExpired()
	if err != nil || count != 1 {
		t.Fatalf("cleanup count=%d err=%v", count, err)
	}
	keys, _ := os.ReadFile(config.AuthorizedKeysFile)
	if strings.Contains(string(keys), grant.ClientID) {
		t.Fatalf("expired client remains authorized: %s", keys)
	}
}

func TestRenderAccessLockedBranchesOnKind(t *testing.T) {
	manager, config := newActivationTestManager(t)
	config.MobileEntryPath = "/usr/local/libexec/filees/filees-mobile-v1"
	manager, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}

	desktop := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := manager.Stage(desktop); err != nil {
		t.Fatal(err)
	}
	mobile := testActivationGrant(t, time.Now().Add(time.Hour))
	mobile.Kind = onboarding.KindMobile
	if err := manager.Stage(mobile); err != nil {
		t.Fatal(err)
	}

	keys, err := os.ReadFile(config.AuthorizedKeysFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(keys), `restrict,command="`+config.ClientEntryPath+" "+desktop.OperationID+" "+desktop.ClientID+`"`) {
		t.Fatalf("desktop (empty Kind) command line missing or wrong: %s", keys)
	}
	if !strings.Contains(string(keys), `restrict,command="`+config.MobileEntryPath+" "+mobile.OperationID+" "+mobile.ClientID+`"`) {
		t.Fatalf("mobile command line missing or wrong: %s", keys)
	}

	// Mobile records still get an authz stanza too (deliberate, see
	// concepts/FILEES_ANDROID_CLIENT_CONCEPT_V2.md S3.1 - harmless and not
	// worth filtering).
	authz, err := os.ReadFile(config.AuthzFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(authz), mobile.ClientID+" = r") {
		t.Fatalf("mobile client missing from authz: %s", authz)
	}
}

func TestRenderAccessLockedRequiresMobileEntryPathConfigured(t *testing.T) {
	manager, _ := newActivationTestManager(t)
	grant := testActivationGrant(t, time.Now().Add(time.Hour))
	grant.Kind = onboarding.KindMobile
	if err := manager.Stage(grant); err == nil {
		t.Fatal("staged a mobile record with mobile_entry_path unconfigured")
	}
}

func TestSameGrantRejectsMismatchedKind(t *testing.T) {
	manager, config := newActivationTestManager(t)
	config.MobileEntryPath = "/usr/local/libexec/filees/filees-mobile-v1"
	manager, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant := testActivationGrant(t, time.Now().Add(time.Hour))
	if err := manager.Stage(grant); err != nil {
		t.Fatal(err)
	}
	resumed := grant
	resumed.Kind = onboarding.KindMobile
	if err := manager.Stage(resumed); err == nil {
		t.Fatal("resume with a different Kind than the original stage was accepted")
	}
}

func newActivationTestManager(t *testing.T) (*Manager, Config) {
	t.Helper()
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn unavailable")
	}
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin unavailable")
	}
	svnserve, err := exec.LookPath("svnserve")
	if err != nil {
		t.Skip("svnserve unavailable")
	}
	root := t.TempDir()
	repository, wc := filepath.Join(root, "repository"), filepath.Join(root, "wc")
	runActivationCommand(t, svnadmin, "create", repository)
	runActivationCommand(t, svn, "mkdir", "--non-interactive", "--no-auth-cache", "-m", "init proof", "file://"+repository+"/proof")
	runActivationCommand(t, svn, "checkout", "--non-interactive", "--no-auth-cache", "file://"+repository, wc)
	config := Config{
		Root: filepath.Join(root, "activation"), AuthorizedKeysFile: filepath.Join(root, "authorized_keys"),
		AuthzFile: filepath.Join(root, "authz"), ServiceWorkingCopy: wc, ServiceRepository: repository,
		RepositoryName: "filees-service", ClientEntryPath: "/usr/local/libexec/filees/filees-client-entry",
		SVNBinary: svn, SVNServeBinary: svnserve,
	}
	manager, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager, config
}

func testActivationGrant(t *testing.T, expires time.Time) onboarding.ActivationGrant {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return onboarding.ActivationGrant{
		OperationID: uuid.NewString(), DeployRequestID: uuid.NewString(), ClientID: uuid.NewString(), RealmID: uuid.NewString(),
		InstallationPublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " filees:test",
		InstallationFingerprint: ssh.FingerprintSHA256(key), ExpiresAt: expires,
	}
}

func runActivationCommand(t *testing.T, command string, args ...string) {
	t.Helper()
	if output, err := exec.Command(command, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", command, args, err, output)
	}
}
