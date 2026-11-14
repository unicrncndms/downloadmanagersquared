package winutil

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var pShellExecuteEx = shell32.NewProc("ShellExecuteExW")

// ErrElevationDeclined is the user saying no to Windows' administrator prompt.
var ErrElevationDeclined = errors.New("administrator rights were not given")

type shellExecuteInfo struct {
	size      uint32
	mask      uint32
	hwnd      windows.Handle
	verb      *uint16
	file      *uint16
	params    *uint16
	dir       *uint16
	show      int32
	instApp   windows.Handle
	idList    uintptr
	class     *uint16
	hkeyClass windows.Handle
	hotKey    uint32
	iconOrMon windows.Handle
	process   windows.Handle
}

// RunElevated starts exe with params as administrator, which makes Windows
// ask the user. With wait, it returns the process's exit code once it exits.
func RunElevated(exe, params string, wait bool) (uint32, error) {
	info := shellExecuteInfo{
		mask:   0x40 | 0x100, // SEE_MASK_NOCLOSEPROCESS | SEE_MASK_NOASYNC
		verb:   wstr("runas"),
		file:   wstr(exe),
		params: wstr(params),
		dir:    wstr(os.TempDir()),
		show:   windows.SW_HIDE,
	}
	info.size = uint32(unsafe.Sizeof(info))
	if r, _, err := pShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return 0, ErrElevationDeclined
		}
		return 0, err
	}
	if info.process == 0 {
		return 0, nil
	}
	defer windows.CloseHandle(info.process)
	if !wait {
		return 0, nil
	}
	windows.WaitForSingleObject(info.process, windows.INFINITE)
	var code uint32
	err := windows.GetExitCodeProcess(info.process, &code)
	return code, err
}

// HoldUnchanged opens path so that nobody can write, replace or rename it (or
// its folders) until the returned file is closed. Install holds the exe it
// elevates so a same-user process can't swap it before the prompt is answered.
func HoldUnchanged(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// CanWrite is whether this process can create files in dir, or in the nearest
// folder above it that exists (where dir would be created).
func CanWrite(dir string) bool {
	dir = filepath.Clean(dir)
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			break
		}
		up := filepath.Dir(dir)
		if up == dir {
			return false
		}
		dir = up
	}
	f, err := os.CreateTemp(dir, ".dms-write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}
