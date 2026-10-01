//go:build windows

package msistartup

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

type process struct {
	handle windows.Handle
	pid    uint32
}

func (p *process) Close() error { return windows.CloseHandle(p.handle) }
func (p *process) Exited() (bool, uint32, error) {
	state, err := windows.WaitForSingleObject(p.handle, 0)
	if err != nil {
		return false, 0, err
	}
	if state == uint32(windows.WAIT_TIMEOUT) {
		return false, 0, nil
	}
	if state != windows.WAIT_OBJECT_0 {
		return false, 0, fmt.Errorf("unexpected process wait %d", state)
	}
	var code uint32
	err = windows.GetExitCodeProcess(p.handle, &code)
	return true, code, err
}

func commandLine(handle windows.Handle) ([]string, error) {
	buf := make([]byte, 4096)
	for {
		var needed uint32
		err := windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &needed)
		if err == nil {
			return windows.DecomposeCommandLine((*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String())
		}
		if !errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) || needed <= uint32(len(buf)) || needed > 1<<20 {
			return nil, err
		}
		buf = make([]byte, needed)
	}
}

func sameUser(handle windows.Handle) bool {
	var token windows.Token
	if windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token) != nil {
		return false
	}
	defer token.Close()
	other, err := token.GetTokenUser()
	if err != nil {
		return false
	}
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && self.User.Sid.Equals(other.User.Sid)
}

// Find only adopts this user's daemon in this session and installation.
// A renamed running EXE is valid; an unrelated installation or CLI isn't.
func findDaemon(root string) (*process, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return nil, err
	}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if name != "filees.exe" && !strings.HasPrefix(name, "filees.exe.superseded-") {
			continue
		}
		var candidateSession uint32
		if windows.ProcessIdToSessionId(entry.ProcessID, &candidateSession) != nil || candidateSession != session {
			continue
		}
		h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, entry.ProcessID)
		if e != nil {
			continue
		}
		image := make([]uint16, windows.MAX_LONG_PATH)
		size := uint32(len(image))
		args, lineErr := commandLine(h)
		valid := windows.QueryFullProcessImageName(h, 0, &image[0], &size) == nil && sameUser(h) && lineErr == nil && len(args) >= 2 && args[1] == "daemon" &&
			strings.EqualFold(filepath.Clean(filepath.Dir(windows.UTF16ToString(image[:size]))), filepath.Clean(root))
		if valid {
			p := &process{handle: h, pid: entry.ProcessID}
			if exited, _, err := p.Exited(); err == nil && !exited {
				return p, nil
			}
		}
		windows.CloseHandle(h)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return nil, nil
}
