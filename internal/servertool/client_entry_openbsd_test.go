//go:build openbsd

package servertool

import (
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"filees/internal/obsandbox"
	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"
)

func TestMailAfterControlUnderLockedClientEntryUnveil(t *testing.T) {
	if os.Getenv("FILEES_CONTROL_MAIL_NATIVE") == "1" {
		root := os.Getenv("FILEES_CONTROL_MAIL_ROOT")
		entryPromises := clientEntryPromises + " inet dns"
		if err := sandboxBegin(entryPromises); err != nil {
			t.Fatal(err)
		}
		profile := obsandbox.Profile{
			Name:     "filees-client-entry/control-mail-test",
			Promises: entryPromises,
			Paths:    []obsandbox.Path{{Label: "onboarding-root", Name: root, Perms: "rwc"}},
		}
		if err := sandboxApplyForExec(profile, workerPromises+" dns unveil"); err != nil {
			t.Fatal(err)
		}
		config := serverconfig.Config{Root: root, Onboarding: onboarding.Options{
			OperationTTL: time.Minute, OTPAttempts: 1,
			ReversePortFirst: 42000, ReversePortLast: 42000,
		}}
		if err := deliverMailAfterControl(config, io.Discard); err != nil {
			t.Fatal(err)
		}
		return
	}

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := onboarding.Initialize(root); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMailAfterControlUnderLockedClientEntryUnveil$")
	command.Env = append(os.Environ(), "FILEES_CONTROL_MAIL_NATIVE=1", "FILEES_CONTROL_MAIL_ROOT="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native control-mail child: %v: %s", err, output)
	}
}
