package winutil

import (
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Dialog shows a message box and returns the button pressed (windows.IDOK…).
func Dialog(title, text string, style uint32) int32 {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	r, _ := windows.MessageBox(0, t, c, style|windows.MB_SETFOREGROUND)
	return r
}

// ProcessesOf are the running processes started from exe, other than this one.
func ProcessesOf(exe string) []uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	self := windows.GetCurrentProcessId()
	base := strings.ToLower(exe[strings.LastIndexAny(exe, `\/`)+1:])
	var out []uint32
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ProcessID == self || strings.ToLower(windows.UTF16ToString(pe.ExeFile[:])) != base {
			continue
		}
		if p, err := processExe(pe.ProcessID); err == nil && strings.EqualFold(p, exe) {
			out = append(out, pe.ProcessID)
		}
	}
	return out
}

// WaitExit waits until the process has exited, or the timeout passes.
func WaitExit(pid uint32, timeout time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return true // already gone
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	return ev == windows.WAIT_OBJECT_0
}

// StartDetached starts exe with args as a process of its own, outside any job
// object of this one (a browser's, for dms-host), so it outlives its starter.
func StartDetached(exe string, args ...string) error {
	var err error
	for _, flags := range []uint32{windows.CREATE_BREAKAWAY_FROM_JOB | windows.DETACHED_PROCESS, windows.DETACHED_PROCESS} {
		cmd := exec.Command(exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if err = cmd.Start(); err == nil {
			return cmd.Process.Release()
		}
	}
	return err
}

// OpenInFirefox opens url in Firefox, found as "start firefox" would find it,
// else in the default browser.
func OpenInFirefox(url string) error {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\App Paths\firefox.exe`, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		exe, _, err := k.GetStringValue("")
		k.Close()
		if err == nil && exec.Command(exe, url).Start() == nil {
			return nil
		}
	}
	return ShellOpen(url)
}

// Buttons Dialog returns.
const (
	IDOK  = 1
	IDYES = 6
)

var pAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")

// AllowForeground lets any process bring a window to the front, as this one
// (started by the user) may: so the running engine's window, asked for by a
// second launch, comes up in front rather than flashing in the taskbar.
func AllowForeground() {
	const asfwAny = ^uintptr(0) // ASFW_ANY, (DWORD)-1
	pAllowSetForegroundWindow.Call(asfwAny & 0xFFFFFFFF)
}
