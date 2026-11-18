package winutil

import (
	"errors"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

// The update guard: an install or uninstall holds it from quitting the
// engine until its files are in place, and dms-host waits for it before
// starting the engine, so a browser that reconnects meanwhile doesn't start
// the old exe and lock it against the copy. A named mutex: one the holder
// leaves behind by exiting counts as released.
var updateGuard = `Local\downloadmanagersquared-updating`

// HoldUpdate takes the update guard; release gives it back. Call release on
// the same goroutine: a mutex belongs to the thread that took it, so the
// goroutine stays on its thread until then.
func HoldUpdate() (release func()) {
	runtime.LockOSThread()
	name, _ := windows.UTF16PtrFromString(updateGuard)
	h, err := windows.CreateMutex(nil, true, name)
	if h == 0 {
		runtime.UnlockOSThread()
		return func() {}
	}
	owned := !errors.Is(err, windows.ERROR_ALREADY_EXISTS) // another install holds it: ours only keeps it alive
	return func() {
		if owned {
			windows.ReleaseMutex(h)
		}
		windows.CloseHandle(h)
		runtime.UnlockOSThread()
	}
}

// WaitUpdate waits, at most max, while an install or uninstall holds the
// update guard. held: one did (it may be over, or still running after max).
func WaitUpdate(max time.Duration) (held bool) {
	name, _ := windows.UTF16PtrFromString(updateGuard)
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false // none: no update running
	}
	defer windows.CloseHandle(h)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ev, _ := windows.WaitForSingleObject(h, uint32(max/time.Millisecond))
	if ev == windows.WAIT_OBJECT_0 || ev == windows.WAIT_ABANDONED {
		windows.ReleaseMutex(h) // the wait took it: give it straight back
	}
	return true
}
