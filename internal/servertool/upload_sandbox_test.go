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
	"filees/pkg/serverconfig"
)

func TestUploadMaintenanceProfilesSeparateSeedFromReaper(t *testing.T) {
	var config serverconfig.Config
	config.Repositories.Root = "/srv/repos"
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
		for _, path := range profile.Paths {
			if path.Name == "/" || path.Name == config.Activation.ServiceWorkingCopy {
				t.Fatal("overbroad path", path)
			}
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
	config.Repositories.Root = filepath.Join(root, "repos")
	config.Repositories.SVNAdminBinary = "/usr/local/bin/svnadmin"
	config.Repositories.ResultsRoot = filepath.Join(root, "results")
	config.Upload.IntakeRoot = filepath.Join(root, "intake")
	config.Upload.TrashRoot = filepath.Join(root, "trash")
	config.Upload.AVCommand = []string{"/usr/bin/true"}
	for _, dir := range []string{config.Repositories.Root, config.Repositories.ResultsRoot, config.Upload.IntakeRoot, config.Upload.TrashRoot, config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
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
	for _, tool := range []string{config.Repositories.EffectiveSVNMuccBinary(), config.Repositories.EffectiveSVNLookBinary(), config.Upload.AVCommand[0]} {
		if out, err := exec.Command(tool, "--version").CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", tool, err, out)
		}
	}
}
