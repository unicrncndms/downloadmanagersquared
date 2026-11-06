package winutil

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Just enough COM for a shortcut and the folder picker: objects are called
// through their vtables by method index, as the Windows headers declare them.

var (
	ole32                        = windows.NewLazySystemDLL("ole32.dll")
	pCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	pSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")

	clsidShellLink      = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW       = windows.GUID{Data1: 0x000214f9, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile      = windows.GUID{Data1: 0x0000010b, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	clsidFileOpenDialog = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	iidFileOpenDialog   = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
	iidShellItem        = windows.GUID{Data1: 0x43826d1e, Data2: 0xe718, Data3: 0x42ee, Data4: [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe}}
)

// Vtable indices (IUnknown's three methods come first in each).
const (
	mRelease = 2

	mShellLinkSetDescription      = 7
	mShellLinkSetWorkingDirectory = 9
	mShellLinkSetIconLocation     = 17
	mShellLinkSetPath             = 20
	mPersistFileSave              = 6

	mDialogShow       = 3
	mDialogSetOptions = 9
	mDialogGetOptions = 10
	mDialogSetFolder  = 12
	mDialogSetTitle   = 17
	mDialogGetResult  = 20
	mItemDisplayName  = 5

	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	sigdnFileSysPath   = 0x80058000
)

type comObject struct{ vtbl *[32]uintptr }

func (o *comObject) call(method int, args ...uintptr) error {
	r, _, _ := syscall.SyscallN(o.vtbl[method], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return hresult(r)
}

func (o *comObject) release() { o.call(mRelease) }

func hresult(r uintptr) error {
	if int32(r) < 0 {
		return windows.Errno(r)
	}
	return nil
}

// withCOM runs f on one OS thread with COM initialized there (single-threaded,
// as shell objects want).
func withCOM(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	if err == nil || errors.Is(err, windows.Errno(1)) { // S_FALSE: already initialized
		defer windows.CoUninitialize()
	}
	return f()
}

func createInstance(clsid, iid *windows.GUID) (*comObject, error) {
	var o *comObject
	r, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, 1 /* CLSCTX_INPROC_SERVER */, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&o)))
	if err := hresult(r); err != nil {
		return nil, err
	}
	return o, nil
}

func wstr(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

// CreateShortcut writes a .lnk at lnk that starts target, showing icon (an
// .ico file; empty for the target's own).
func CreateShortcut(lnk, target, workDir, desc, icon string) error {
	return withCOM(func() error {
		sl, err := createInstance(&clsidShellLink, &iidShellLinkW)
		if err != nil {
			return err
		}
		defer sl.release()
		strs := []*uint16{wstr(target), wstr(workDir), wstr(desc), wstr(icon), wstr(lnk)}
		defer runtime.KeepAlive(strs)
		for i, m := range []int{mShellLinkSetPath, mShellLinkSetWorkingDirectory, mShellLinkSetDescription} {
			if err := sl.call(m, uintptr(unsafe.Pointer(strs[i]))); err != nil {
				return err
			}
		}
		if icon != "" {
			if err := sl.call(mShellLinkSetIconLocation, uintptr(unsafe.Pointer(strs[3])), 0); err != nil {
				return err
			}
		}
		var pf *comObject
		if err := sl.call(0, uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&pf))); err != nil {
			return err
		}
		defer pf.release()
		return pf.call(mPersistFileSave, uintptr(unsafe.Pointer(strs[4])), 1)
	})
}

// PickFolder shows Windows' folder picker over owner, opened at start. False
// if the user cancelled.
func PickFolder(owner windows.Handle, start, title string) (string, bool) {
	var path string
	withCOM(func() error {
		d, err := createInstance(&clsidFileOpenDialog, &iidFileOpenDialog)
		if err != nil {
			return err
		}
		defer d.release()
		var opts uint32
		if err := d.call(mDialogGetOptions, uintptr(unsafe.Pointer(&opts))); err != nil {
			return err
		}
		if err := d.call(mDialogSetOptions, uintptr(opts|fosPickFolders|fosForceFileSystem)); err != nil {
			return err
		}
		t, s := wstr(title), wstr(start)
		d.call(mDialogSetTitle, uintptr(unsafe.Pointer(t)))
		if start != "" {
			var item *comObject
			r, _, _ := pSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(s)), 0, uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item)))
			if hresult(r) == nil {
				d.call(mDialogSetFolder, uintptr(unsafe.Pointer(item)))
				item.release()
			}
		}
		runtime.KeepAlive(t)
		runtime.KeepAlive(s)
		// In front even when the engine is in the background (frontOwner).
		front, done := frontOwner(owner)
		defer done()
		if err := d.call(mDialogShow, uintptr(front)); err != nil {
			return err // cancelled, mostly
		}
		var item *comObject
		if err := d.call(mDialogGetResult, uintptr(unsafe.Pointer(&item))); err != nil {
			return err
		}
		defer item.release()
		var p *uint16
		if err := item.call(mItemDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&p))); err != nil {
			return err
		}
		path = windows.UTF16PtrToString(p)
		windows.CoTaskMemFree(unsafe.Pointer(p))
		return nil
	})
	return path, path != ""
}
