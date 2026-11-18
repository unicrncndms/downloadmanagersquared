package cdp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"downloadmanagersquared/engine/internal/winutil"
)

// flags every start gets beside the pipe and the profile. Checked on Edge and
// Chrome 157 (.scratch/chromium-support/issues/18-cdp-spike.md).
var flags = []string{
	"--no-first-run",
	"--no-default-browser-check",
	// Edge opens a sync sign-in dialog in a fresh profile without these.
	"--disable-sync",
	"--disable-features=msImplicitSignin,msEdgeSyncConsentDialog",
	// A minimized resolver window must not slow a site's countdown.
	"--disable-background-timer-throttling",
	"--disable-renderer-backgrounding",
	"--disable-backgrounding-occluded-windows",
	// The replayed clicks are the page script's, not the user's: without this a
	// click that opens a tab is blocked (18). Only this browser, never the user's.
	"--disable-popup-blocking",
	// Only the resolves' own windows: no empty one at start (as Playwright
	// drives a headful Chromium).
	"--no-startup-window",
	// Driven over the pipe, Chromium says navigator.webdriver = true, which
	// captcha services take for a bot: a person solves the captcha here
	// (ticket 35). Firefox's resolver windows say false.
	"--disable-blink-features=AutomationControlled",
}

// Exe picks the browser to drive: Chrome, else Edge (every Windows 10/11 has
// it), else any other Chromium. "" when there is none.
func Exe(browsers []winutil.Browser) string {
	pick := ""
	rank := 99
	for _, b := range browsers {
		if !b.Chromium {
			continue
		}
		r := 2
		switch strings.ToLower(filepath.Base(b.Exe)) {
		case "chrome.exe":
			r = 0
		case "msedge.exe":
			r = 1
		}
		if r < rank {
			pick, rank = b.Exe, r
		}
	}
	return pick
}

// Start runs exe with its own profile in profileDir and connects to it.
// onEvent is as for newBrowser. The process ends with Close, or by itself
// when the engine exits and the pipe breaks.
func Start(exe, profileDir string, onEvent func(Event)) (*Browser, error) {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, err
	}
	// The browser reads calls from toR and writes to fromW: the two ends it inherits.
	toR, toW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	fromR, fromW, err := os.Pipe()
	if err != nil {
		toR.Close()
		toW.Close()
		return nil, err
	}
	inherit := []syscall.Handle{syscall.Handle(toR.Fd()), syscall.Handle(fromW.Fd())}
	for _, h := range inherit {
		syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT)
	}
	args := append([]string{
		"--remote-debugging-pipe",
		fmt.Sprintf("--remote-debugging-io-pipes=%d,%d", inherit[0], inherit[1]),
		"--user-data-dir=" + profileDir,
	}, flags...)
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: inherit}
	err = cmd.Start()
	toR.Close()
	fromW.Close()
	if err != nil {
		toW.Close()
		fromR.Close()
		return nil, err
	}
	b := newBrowser(fromR, toW, onEvent)
	b.PID = uint32(cmd.Process.Pid)
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
		b.end(fmt.Errorf("%w: the browser exited", ErrClosed))
	}()
	b.stop = func() {
		// The browser quits when its pipe closes; kill it if it doesn't.
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
		fromR.Close()
	}
	return b, nil
}
