//go:build windows

package msistartup

import (
	"context"
	"errors"
	"filees/pkg/ipcclient"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"
)

// upgradeSupervisor identifies only our interpreter-era or native supervisor.
// A shell command, other installation, or ordinary launcher is not a target.
func upgradeSupervisor(root, image string, args []string) bool {
	if len(args) < 2 {
		return false
	}
	base := strings.ToLower(filepath.Base(image))
	if base == "powershell.exe" {
		for i := 1; i+1 < len(args); i++ {
			if strings.EqualFold(args[i], "-Command") || strings.EqualFold(args[i], "-EncodedCommand") {
				return false
			}
			if strings.EqualFold(args[i], "-File") {
				return filepath.IsAbs(args[i+1]) && strings.EqualFold(filepath.Clean(args[i+1]), filepath.Join(root, "start-filees.ps1")) &&
					(i+2 == len(args) || i+3 == len(args) && strings.EqualFold(args[i+2], "-ShowGUI"))
			}
		}
		return false
	}
	return (base == "filees-launch.exe" || strings.HasPrefix(base, "filees-launch.exe.superseded-")) &&
		strings.EqualFold(filepath.Clean(filepath.Dir(image)), root) && args[1] == "--supervise" &&
		(len(args) == 2 || len(args) == 3 && args[2] == "--interactive")
}

// Hold verified process handles through termination; PID reuse cannot change
// the target. Only the same user/session can be matched. No daemon is killed.
func stopUpgradeSupervisors(ctx context.Context, root string) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return err
	}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if name != "powershell.exe" && name != "filees-launch.exe" && !strings.HasPrefix(name, "filees-launch.exe.superseded-") {
			continue
		}
		var otherSession uint32
		if windows.ProcessIdToSessionId(entry.ProcessID, &otherSession) != nil || otherSession != session {
			continue
		}
		h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, entry.ProcessID)
		if e != nil {
			// Enumeration below and the singleton check must still prove closure.
			continue
		}
		image := make([]uint16, windows.MAX_LONG_PATH)
		size := uint32(len(image))
		args, lineErr := commandLine(h)
		valid := sameUser(h) && lineErr == nil && windows.QueryFullProcessImageName(h, 0, &image[0], &size) == nil && upgradeSupervisor(root, windows.UTF16ToString(image[:size]), args)
		if valid {
			e = windows.TerminateProcess(h, 0)
			if e == nil {
				var state uint32
				state, e = windows.WaitForSingleObject(h, 5000)
				if e == nil && state != windows.WAIT_OBJECT_0 {
					e = errors.New("supervisor exit not confirmed")
				}
			}
		}
		windows.CloseHandle(h)
		if valid && e != nil {
			return fmt.Errorf("stop installation supervisor: %w", e)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	return nil
}

// PrepareUpgrade runs from the NEW MSI's embedded executable, not the old
// installation's launcher. It stops supervisors first so they cannot adopt
// replacements, then waits for the daemon's clean stop (ACK is not exit).
// Any uncertain closure aborts installation before files are replaced.
func PrepareUpgrade(ctx context.Context, p InstallPaths) error {
	root := filepath.Clean(p.Root)
	if !filepath.IsAbs(root) || root == filepath.VolumeName(root)+string(os.PathSeparator) {
		return errors.New("upgrade requires an absolute installation folder, not a volume root")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := stopUpgradeSupervisors(ctx, root); err != nil {
		return err
	}
	proc, err := findDaemon(root)
	if err != nil {
		return err
	}
	if proc != nil {
		defer proc.Close()
		if _, err := ipcclient.New(p.Socket, "filees-msi-upgrade").SystemShutdown(ctx); err != nil {
			return fmt.Errorf("clean shutdown refused; installation unchanged: %w", err)
		}
		for {
			exited, _, err := proc.Exited()
			if err != nil {
				return err
			}
			if exited {
				break
			}
			if err := pause(ctx, 50*time.Millisecond); err != nil {
				return fmt.Errorf("daemon has not finished shutdown; installation unchanged: %w", err)
			}
		}
	}
	if replacement, err := findDaemon(root); err != nil {
		return err
	} else if replacement != nil {
		replacement.Close()
		return errors.New("replacement daemon appeared during shutdown; installation unchanged")
	}
	if running, err := supervisorRunning(p.LockID); err != nil {
		return err
	} else if running {
		return errors.New("installation supervisor still running; installation unchanged")
	}
	return nil
}
