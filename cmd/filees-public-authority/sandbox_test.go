package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filees/internal/obsandbox"
	"filees/internal/storagewatch"
)

func TestAuthoritySandboxAllowsPrivateStagingMode(t *testing.T) {
	for _, promise := range strings.Fields(authoritySandboxPromises) {
		if promise == "fattr" {
			return
		}
	}
	t.Fatal("authority sandbox must permit fattr for staging file chmod")
}

func TestAuthorityGatePathsRemainNarrow(t *testing.T) {
	root := t.TempDir()
	paths := authorityGateSandboxPaths(root)
	want := map[string]string{"capacity-write-policy.json": "r", ".capacity-writers.lock": "rw", "capacity-active-writers": "rwc", "state-observation": "rwc"}
	if len(paths) != len(want) {
		t.Fatal("unexpected gate scope", paths)
	}
	for _, p := range paths {
		name := filepath.Base(p.Name)
		if filepath.Dir(p.Name) != root || want[name] != p.Perms {
			t.Fatal("gate scope widened", p)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatal("missing gate path", want)
	}
}

// Native regression for r2186: Prepare succeeded before unveil, while every
// download's WithWriter subsequently failed before even running its callback.
func TestAuthoritySandboxGuardedSpool(t *testing.T) {
	if runtime.GOOS != "openbsd" {
		t.Skip("native pledge/unveil")
	}
	const env = "FILEES_AUTHORITY_GATE_SANDBOX_FIXTURE"
	root := os.Getenv(env)
	if root == "" {
		root = t.TempDir()
		child := exec.Command(os.Args[0], "-test.run=^TestAuthoritySandboxGuardedSpool$", "-test.v")
		child.Env = append(os.Environ(), env+"="+root)
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("sandbox child: %v\n%s", err, out)
		}
		return
	}
	gate := storagewatch.Gate{Root: root}
	if err := gate.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "staging")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "unrelated-activation-secret")
	if err := os.WriteFile(secret, []byte("fixture only"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := append(authorityGateSandboxPaths(root), obsandbox.Path{Label: "staging", Name: staging, Perms: "rwc"})
	if err := obsandbox.Apply(obsandbox.Profile{Name: "authority-guarded-spool", Promises: authoritySandboxPromises, Paths: paths}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(secret); err == nil {
		t.Fatal("unrelated activation file exposed")
	}
	ran := false
	if err := gate.WithWriter(context.Background(), func() error {
		ran = true
		return os.WriteFile(filepath.Join(staging, "leaf"), []byte("fixture only"), 0600)
	}); err != nil {
		t.Fatalf("guarded spool: %v", err)
	}
	if !ran {
		t.Fatal("spool never ran")
	}
	if _, clean, err := gate.ObservationStamp(); err != nil || !clean {
		t.Fatalf("writer did not durably acknowledge invalidation: clean=%v err=%v", clean, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "capacity-active-writers"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("orphaned writer: %v %v", entries, err)
	}
}
