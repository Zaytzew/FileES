//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"filees/internal/gui/platform"
	"filees/pkg/opjournal"
	"golang.org/x/sys/windows"
)

type browserProcesses struct{ handles map[uint32]windows.Handle }

func (processes *browserProcesses) close() {
	for pid, handle := range processes.handles {
		windows.CloseHandle(handle)
		delete(processes.handles, pid)
	}
}

func (processes *browserProcesses) observe(parent uint32) (bool, string, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, "", err
	}
	defer windows.CloseHandle(snapshot)
	found := make(map[uint32]bool)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snapshot, &entry)
	for err == nil {
		if entry.ParentProcessID == parent && strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "msedgewebview2.exe") {
			found[entry.ProcessID] = true
		}
		err = windows.Process32Next(snapshot, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return false, "", err
	}
	if processes.handles == nil {
		processes.handles = make(map[uint32]windows.Handle)
	}
	var exits []string
	for pid, handle := range processes.handles {
		if found[pid] {
			continue
		}
		var code uint32
		if err := windows.GetExitCodeProcess(handle, &code); err == nil {
			exits = append(exits, fmt.Sprintf("browser_pid=%d exit=0x%08x", pid, code))
		}
		windows.CloseHandle(handle)
		delete(processes.handles, pid)
	}
	for pid := range found {
		if _, tracked := processes.handles[pid]; !tracked {
			handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if err == nil {
				processes.handles[pid] = handle
			}
		}
	}
	return len(found) != 0, strings.Join(exits, "; "), nil
}

// No JS heartbeat: a suspended/minimised page is not a failed browser. The
// native process snapshot continues to work when the entire WebView is gone.
func watchDisplayHealth(ctx context.Context, profile string, service *GUIService, backend platform.Backend, recoverGUI func() bool, restart func()) {
	dir, err := opjournal.Dir()
	if err != nil {
		return
	} // No durable diagnosis/budget: no automatic restart.
	budget := filepath.Join(profile, "gui-display-recovery.json")
	recordDisplayHealth(dir, "watch_started", "build="+clientVersion()+"; browser-loss only")
	tracker := &browserProcesses{}
	defer tracker.close()
	health := displayHealth{}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	loggedLoss, loggedBusy, loggedProbe := false, false, false
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			present, exit, probeErr := tracker.observe(uint32(os.Getpid()))
			if ctx.Err() != nil {
				return
			}
			if exit != "" {
				recordDisplayHealth(dir, "browser_exit", exit)
			}
			if probeErr != nil && !loggedProbe {
				recordDisplayHealth(dir, "probe_failed", "process enumeration unavailable")
				loggedProbe = true
			}
			if probeErr == nil {
				loggedProbe = false
			}
			if !health.lost(now, present, probeErr) {
				if present {
					loggedLoss, loggedBusy = false, false
				}
				continue
			}
			if !loggedLoss {
				recordDisplayHealth(dir, "browser_missing", "previously observed browser absent for 20 seconds; cause unknown")
				loggedLoss = true
			}
			if _, err := displayRecoveryAvailable(budget, now); err != nil {
				recordDisplayHealth(dir, "recovery_stopped", err.Error())
				_ = backend.Notify(ctx, platform.Notification{ID: "display-recovery", Group: "interface", Title: "FileES", Body: service.localizeText("display.recoveryStopped", "FileES cannot restore its window automatically. Restart only the panel; the daemon is still running."), Urgency: platform.UrgencyCritical})
				return
			}
			// Quiescence is checked before spending a restart. Success closes
			// admission, and the existing host shutdown follows immediately.
			if !recoverGUI() {
				if !loggedBusy {
					recordDisplayHealth(dir, "recovery_deferred", "active task or secondary window; no cancellation")
					loggedBusy = true
					_ = backend.Notify(ctx, platform.Notification{ID: "display-recovery", Group: "interface", Title: "FileES", Body: service.localizeText("display.recoveryDeferred", "The FileES window is unavailable. Automatic recovery is waiting for an operation or an open dialog. The daemon continues working."), Urgency: platform.UrgencyCritical})
				}
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if err := takeDisplayRecovery(budget, now); err != nil {
				recordDisplayHealth(dir, "recovery_stopped", err.Error())
				_ = backend.Notify(ctx, platform.Notification{ID: "display-recovery", Group: "interface", Title: "FileES", Body: service.localizeText("display.recoveryStopped", "FileES cannot restore its window automatically. Restart only the panel; the daemon is still running."), Urgency: platform.UrgencyCritical})
				return
			}
			recordDisplayHealth(dir, "recovery_requested", "GUI-only restart; no intent replay; daemon unchanged")
			_ = backend.Notify(ctx, platform.Notification{ID: "display-recovery", Group: "interface", Title: "FileES", Body: service.localizeText("display.recovering", "FileES is restoring its window. The daemon continues working."), Urgency: platform.UrgencyNormal})
			restart()
			return
		}
	}
}
