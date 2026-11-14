// Package winutil wraps the small Windows facilities the engine needs.
package winutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "downloadmanagersquared"

// SetSparse marks a file sparse (or back to normal), so a huge file can be
// sized instantly without NTFS zero-filling it.
func SetSparse(f *os.File, on bool) error {
	var in byte
	if on {
		in = 1
	}
	var ret uint32
	return windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, &in, 1, nil, 0, &ret, nil)
}

// FreeSpace is the number of bytes the current user may still write under dir.
func FreeSpace(dir string) (int64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return int64(free), nil
}

func DownloadsFolder() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

// Hidden makes a child process run without a console window.
func Hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

// ShowInFolder opens Explorer with the file selected.
func ShowInFolder(path string) error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	return cmd.Start()
}

func Autostart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

func SetAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValue, `"`+exe+`"`)
}

// ErrShown is a failure the user was already told about, in a message box of
// its own, so whoever gets it shows nothing more.
var ErrShown = errors.New("already shown to the user")

func MessageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	windows.MessageBox(0, t, c, windows.MB_OK|windows.MB_ICONERROR)
}

// ProcessUser is the SID of the user a process runs as. The engine accepts
// pipe and proxy clients only from processes of its own user.
func ProcessUser(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// CurrentUserSID is used to lock the engine's named pipe to this user.
func CurrentUserSID() (string, error) {
	var tok windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok)
	if err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// OpenFile opens path with its default app, as a double click in Explorer does.
func OpenFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(path), nil, nil, windows.SW_SHOWNORMAL)
}

// Recycle moves a file or folder to the Recycle Bin, with no dialog.
func Recycle(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	from, err := windows.UTF16FromString(path)
	if err != nil {
		return err
	}
	from = append(from, 0) // the list ends in a second NUL
	op := struct {
		Wnd     windows.Handle
		Func    uint32
		From    *uint16
		To      *uint16
		Flags   uint16
		Aborted int32
		Maps    uintptr
		Title   *uint16
	}{Func: 3 /* FO_DELETE */, From: &from[0], Flags: 0x40 | 0x10 | 0x4 | 0x400 /* ALLOWUNDO, NOCONFIRMATION, SILENT, NOERRORUI */}
	r, _, _ := shell32.NewProc("SHFileOperationW").Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 || op.Aborted != 0 {
		return fmt.Errorf("moving %s to the Recycle Bin failed (code %#x)", filepath.Base(path), r)
	}
	return nil
}
