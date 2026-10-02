//go:build windows

package msistartup

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestUpgradeSupervisorTargetIsExact(t *testing.T) {
	root := `C:\FileES`
	for _, tc := range []struct {
		image string
		args  []string
		want  bool
	}{
		{`C:\Windows\powershell.exe`, []string{"powershell", "-NoProfile", "-File", `C:\FileES\start-filees.ps1`}, true},
		{`C:\Windows\powershell.exe`, []string{"powershell", "-File", `C:\Other\start-filees.ps1`}, false},
		{`C:\Windows\powershell.exe`, []string{"powershell", "-Command", "-File", `C:\FileES\start-filees.ps1`}, false},
		{`C:\Windows\powershell.exe`, []string{"powershell", "-File", `C:\FileES\start-filees.ps1`, "arbitrary"}, false},
		{`C:\FileES\filees-launch.exe`, []string{"launcher", "--supervise", "--interactive"}, true},
		{`C:\Other\filees-launch.exe`, []string{"launcher", "--supervise"}, false},
		{`C:\FileES\filees-launch.exe`, []string{"launcher", "--show"}, false},
	} {
		if got := upgradeSupervisor(root, tc.image, tc.args); got != tc.want {
			t.Fatalf("%s %q = %v", tc.image, tc.args, got)
		}
	}
}

func TestUpgradeWaitsForExitNotShutdownAck(t *testing.T) {
	p := startupFixture(t)
	t.Setenv("FILEES_MSI_TEST_STOP_DELAY_MS", "300")
	proc, err := startDaemon(p)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	waitUntil(t, func() bool { return ready(p) })
	start := time.Now()
	if err := PrepareUpgrade(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("shutdown ACK mistaken for exit")
	}
	if exited, _, err := proc.Exited(); err != nil || !exited {
		t.Fatal("still running", err)
	}
}

func TestUpgradeTimeoutNeverKillsDaemon(t *testing.T) {
	p := startupFixture(t)
	t.Setenv("FILEES_MSI_TEST_STOP_DELAY_MS", "400")
	proc, err := startDaemon(p)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	waitUntil(t, func() bool { return ready(p) })
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	if err := PrepareUpgrade(ctx, p); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if exited, _, err := proc.Exited(); err != nil || exited {
		t.Fatal("daemon killed on timeout", err)
	}
	waitUntil(t, func() bool { exited, _, _ := proc.Exited(); return exited })
}

func TestUpgradeStopsOnlyThisLegacyScript(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	var commands []*exec.Cmd
	for _, dir := range []string{root, other} {
		script := filepath.Join(dir, "start-filees.ps1")
		if err := os.WriteFile(script, []byte("New-Item -ItemType File -Path (Join-Path $PSScriptRoot 'started') | Out-Null\nStart-Sleep -Seconds 60\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := hidden(filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), dir, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		waitUntil(t, func() bool { _, err := os.Stat(filepath.Join(dir, "started")); return err == nil })
	}
	if err := stopUpgradeSupervisors(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	// Wait returns only after our exact target exits; the unrelated shell lives.
	if err := commands[0].Wait(); err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(commands[1].Process.Pid))
	if err != nil {
		t.Fatal("unrelated supervisor killed", err)
	}
	h := &process{handle: handle, pid: uint32(commands[1].Process.Pid)}
	defer h.Close()
	if exited, _, err := h.Exited(); err != nil || exited {
		t.Fatal("unrelated supervisor killed", err)
	}
}
