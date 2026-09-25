//go:build windows

package predecessor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// MSIUpgradeCode identifies every FileES desktop MSI regardless of version; it
// must stay equal to UpgradeCode in packaging/windows/filees.wxs, which a test
// enforces. Asking Windows Installer by this code, instead of reading the
// Uninstall registry key, is what keeps an unrelated product that happens to
// be called FileES - the legacy "FileES GUI" MSI still installed on some
// machines has its own code - from being removed by mistake.
const MSIUpgradeCode = "{9E4A1F52-8C1D-4E63-9F0A-6B7D2C5A81E4}"

// StorePackageName is the Partner Center identity (store-identity.json).
const StorePackageName = "FileES.FileESDesktop"

// MSIInstallDir is where the per-user MSI installs, under %LOCALAPPDATA%.
func MSIInstallDir(localAppData string) (string, error) {
	if !filepath.IsAbs(localAppData) {
		return "", errors.New("LOCALAPPDATA is missing or not absolute")
	}
	return filepath.Join(localAppData, "Programs", "FileES"), nil
}

// MSIPresent reports whether the MSI variant's daemon is on disk. The daemon,
// not the directory: the directory also holds config.json and logs, which an
// uninstall deliberately leaves behind.
func MSIPresent(localAppData string) (bool, error) {
	dir, err := MSIInstallDir(localAppData)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(dir, "filees.exe"))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

var procMsiEnumRelatedProducts = windows.NewLazySystemDLL("msi.dll").NewProc("MsiEnumRelatedProductsW")

// MSIProductCodes lists the installed products carrying MSIUpgradeCode.
func MSIProductCodes() ([]string, error) {
	upgrade, err := syscall.UTF16PtrFromString(MSIUpgradeCode)
	if err != nil {
		return nil, err
	}
	var codes []string
	for index := uint32(0); ; index++ {
		buf := make([]uint16, 39) // a GUID in braces plus the terminator
		ret, _, _ := procMsiEnumRelatedProducts.Call(uintptr(unsafe.Pointer(upgrade)), 0, uintptr(index), uintptr(unsafe.Pointer(&buf[0])))
		switch syscall.Errno(ret) {
		case 0:
			codes = append(codes, syscall.UTF16ToString(buf))
		case windows.ERROR_NO_MORE_ITEMS:
			return codes, nil
		default:
			return nil, fmt.Errorf("MsiEnumRelatedProducts: %w", syscall.Errno(ret))
		}
	}
}

// UninstallMSI removes one product quietly. The MSI is per-user, so no
// elevation is requested; 3010 means Windows wants a restart to finish, which
// is success for our purposes - the files the other variant needs are gone.
func UninstallMSI(ctx context.Context, productCode string) error {
	if len(productCode) != 38 || productCode[0] != '{' || productCode[37] != '}' {
		return fmt.Errorf("not a product code: %q", productCode)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(systemDirectory(), "msiexec.exe"), "/x", productCode, "/qn", "/norestart")
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3010 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("msiexec /x %s: %w", productCode, err)
	}
	return nil
}

// StorePackageLocation returns the install folder of the Store package for
// the current user, or "" when it is not installed.
func StorePackageLocation(ctx context.Context) (string, error) {
	out, err := powershell(ctx, "(Get-AppxPackage -Name '"+StorePackageName+"').InstallLocation")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RemoveStorePackage uninstalls the Store package for the current user; like
// the per-user MSI it needs no elevation.
func RemoveStorePackage(ctx context.Context) error {
	_, err := powershell(ctx, "Get-AppxPackage -Name '"+StorePackageName+"' | Remove-AppxPackage")
	return err
}

// The commands are fixed strings with no caller input, so nothing reaches the
// shell that the caller chose.
func powershell(ctx context.Context, command string) (string, error) {
	program := filepath.Join(systemDirectory(), "WindowsPowerShell", "v1.0", "powershell.exe")
	out, err := exec.CommandContext(ctx, program, "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("powershell: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func systemDirectory() string {
	dir, err := windows.GetSystemDirectory()
	if err != nil || dir == "" {
		return `C:\Windows\System32`
	}
	return dir
}

// scriptHosts run the MSI supervisor (start-filees.ps1) and its legacy shim
// (start-filees.vbs). Their image lives in System32, so TerminateUnder never
// sees them.
var scriptHosts = map[string]bool{"powershell.exe": true, "pwsh.exe": true, "wscript.exe": true, "cscript.exe": true}

// TerminateScriptsFrom ends script hosts of this user whose command line
// names a file inside dir. It runs before the daemon is stopped: in the
// sandbox acceptance of 2026-09-25 the MSI supervisor outlived the uninstall
// and adopted the Store daemon as its own replacement (supervisor.log
// "replacement daemon adopted" after its files were gone). Any other script
// host - one that merely runs elsewhere - is left alone.
func TerminateScriptsFrom(dir string) (int, error) {
	if !filepath.IsAbs(dir) {
		return 0, errors.New("directory must be absolute")
	}
	prefix := strings.ToLower(filepath.Clean(dir)) + string(filepath.Separator)
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, fmt.Errorf("list processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	self := windows.GetCurrentProcessId()
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	terminated := 0
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == self || entry.ProcessID == 0 || !scriptHosts[strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))] {
			continue
		}
		handle, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, entry.ProcessID)
		if openErr != nil {
			continue
		}
		if line, lineErr := processCommandLine(handle); lineErr == nil && strings.Contains(strings.ToLower(line), prefix) {
			if windows.TerminateProcess(handle, 1) == nil {
				terminated++
			}
		}
		windows.CloseHandle(handle)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return terminated, fmt.Errorf("walk processes: %w", err)
	}
	return terminated, nil
}

// processCommandLine reads another process's command line
// (ProcessCommandLineInformation, Windows 8.1+): a UNICODE_STRING whose
// buffer follows it in the same allocation.
func processCommandLine(handle windows.Handle) (string, error) {
	buf := make([]byte, 4096)
	for {
		var needed uint32
		err := windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &needed)
		if err == nil {
			text := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
			return text.String(), nil
		}
		if !errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) || needed <= uint32(len(buf)) || needed > 1<<20 {
			return "", err
		}
		buf = make([]byte, needed)
	}
}

// TerminateUnder ends every process of this user whose image lies inside dir,
// except the caller. It runs after the daemon has been asked to stop and has
// stopped; what is left is the other variant's window and supervisor, which
// hold files the uninstall has to remove. Processes of other users cannot be
// opened with these rights and are skipped, not an error.
func TerminateUnder(dir string) (int, error) {
	if !filepath.IsAbs(dir) {
		return 0, errors.New("directory must be absolute")
	}
	prefix := strings.ToLower(filepath.Clean(dir)) + string(filepath.Separator)
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, fmt.Errorf("list processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	self := windows.GetCurrentProcessId()
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	terminated := 0
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == self || entry.ProcessID == 0 {
			continue
		}
		handle, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, entry.ProcessID)
		if openErr != nil {
			continue
		}
		image := make([]uint16, windows.MAX_LONG_PATH)
		size := uint32(len(image))
		if windows.QueryFullProcessImageName(handle, 0, &image[0], &size) == nil &&
			strings.HasPrefix(strings.ToLower(windows.UTF16ToString(image[:size])), prefix) {
			if windows.TerminateProcess(handle, 1) == nil {
				terminated++
			}
		}
		windows.CloseHandle(handle)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return terminated, fmt.Errorf("walk processes: %w", err)
	}
	return terminated, nil
}
