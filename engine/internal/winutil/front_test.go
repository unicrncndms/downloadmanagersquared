package winutil

import (
	"runtime"
	"testing"
)

// frontOwner makes a window to own a dialog and destroys it again; with no
// window of ours in front, it never hands back the window it was given.
func TestFrontOwnerMakesAndDestroysAWindow(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h, done := frontOwner(0)
	if h == 0 {
		t.Fatal("no owner window")
	}
	if v, _, _ := pIsWindowVisible.Call(uintptr(h)); v == 0 {
		t.Fatal("the owner window isn't shown")
	}
	done()
	if v, _, _ := pIsWindowVisible.Call(uintptr(h)); v != 0 {
		t.Fatal("the owner window is still there")
	}
}
