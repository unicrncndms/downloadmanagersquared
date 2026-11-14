// Package tray is the engine's notification-area icon.
package tray

import (
	"fmt"
	"math"
	"strings"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/download"
	"downloadmanagersquared/engine/internal/engine"
	"downloadmanagersquared/engine/internal/window"
	"downloadmanagersquared/engine/internal/winutil"
)

const ifaceRows = 6 // interface lines in the tooltip

// Window is the Downloads window (internal/window). The menu opens it
// through the engine (Engine.OpenWindow), which is given it with SetWindow.
type Window interface {
	Close()
}

// Run blocks until the user quits from the tray menu. An open window is
// closed first.
func Run(e *engine.Engine, w Window) {
	systray.Run(func() { onReady(e, w) }, e.Shutdown)
}

func onReady(e *engine.Engine, w Window) {
	systray.SetIcon(Icon())
	winutil.SetBalloonIcon(Icon())
	systray.SetTooltip(winutil.AppName)
	dark := winutil.AppsDark()
	winutil.SetMenusDark(dark)

	// A left click opens the downloads; a right click shows the menu.
	systray.SetOnTapped(func() { go openDownloads(e) })

	open := systray.AddMenuItem("Show downloads", "Show the downloads")
	clip := systray.AddMenuItem("Add link from clipboard", "Add the http, https or magnet links on the clipboard")
	settings := systray.AddMenuItem("Connections & settings", "Show the connections and settings")
	systray.AddSeparator()
	pause := newLine(systray.AddMenuItem("Pause all", ""))
	resume := newLine(systray.AddMenuItem("Resume all", ""))
	pause.enable(false)
	resume.enable(false)
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit (downloads stop)", "Stop all downloads and exit")

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				go openDownloads(e)
			case <-clip.ClickedCh:
				go addFromClipboard(e)
			case <-settings.ClickedCh:
				go openSettings(e)
			case <-pause.item.ClickedCh:
				go e.PauseAll()
			case <-resume.item.ClickedCh:
				go e.ResumeAll()
			case <-quit.ClickedCh:
				if w := e.RestartCost().Warning("Quitting now"); w != "" && winutil.Dialog(winutil.AppName, w+"\n\nQuit anyway?",
					windows.MB_OKCANCEL|windows.MB_ICONWARNING|windows.MB_DEFBUTTON2) != winutil.IDOK {
					continue
				}
				e.QuitByUser()
				w.Close()
				systray.Quit()
				return
			case <-e.QuitRequested():
				w.Close()
				systray.Quit()
				return
			}
		}
	}()

	go func() {
		used := map[string]time.Time{}
		shown := iconIdle
		for now := range time.Tick(time.Second) {
			if d := winutil.AppsDark(); d != dark {
				dark = d
				winutil.SetMenusDark(dark)
			}
			snap := e.Snapshot()
			m := menuFor(snap, e.SpeedUnit(), used, now)
			if snap.Active > 0 && e.KeepAwake() {
				winutil.StayAwake()
			}
			if st := iconFor(snap); st != shown {
				shown = st
				systray.SetIcon(icons[st]())
			}
			pause.enable(m.Pause)
			resume.enable(m.Resume)

			// Starts with the app name: balloons are named after the tooltip.
			t := strings.Join(append([]string{winutil.AppName + "  " + m.Status}, m.Rows...), "\n")
			// Windows cuts tray tooltips at 127 characters.
			if len(t) > 127 {
				t = t[:127]
			}
			systray.SetTooltip(t)
		}
	}()
}

// line is a menu item that is changed only when its state does: every
// change redraws an open menu, so updating it each second made it flicker.
type line struct {
	item    *systray.MenuItem
	enabled bool
}

func newLine(it *systray.MenuItem) *line { return &line{item: it, enabled: true} }

func (l *line) enable(on bool) {
	if on == l.enabled {
		return
	}
	l.enabled = on
	if on {
		l.item.Enable()
	} else {
		l.item.Disable()
	}
}

// linger is how long an interface keeps its line after it stops carrying
// a download, so a moment without connections doesn't make it blink.
const linger = 5 * time.Second

// menu is what the tray menu shows.
type menu struct {
	Status        string   // "Idle", "Paused", or the speed and time left
	Rows          []string // the interfaces carrying downloads, with their speeds
	Pause, Resume bool     // whether "Pause all" and "Resume all" have something to do (else they are grayed out)
}

// menuFor is the menu for s. used remembers when each interface last
// carried a download; menuFor updates it.
func menuFor(s engine.StateEvent, unit string, used map[string]time.Time, now time.Time) menu {
	var m menu
	for _, d := range s.Downloads {
		switch d.Status {
		case download.StatusPaused, download.StatusFailed:
			m.Resume = true
		case download.StatusCompleted, download.StatusChoosing:
		default:
			m.Pause = true
		}
	}
	switch {
	case s.Active > 0:
		m.Status = fmt.Sprintf("%s  ·  %s left", Speed(s.Speed, unit), Duration(s.ETA))
	case m.Resume && !m.Pause:
		m.Status = "Paused"
	default:
		m.Status = "Idle"
	}
	if s.Active == 0 {
		clear(used)
		return m
	}
	for _, d := range s.Downloads {
		if d.Status != download.StatusRunning {
			continue
		}
		for id, l := range d.Links {
			if l.Conns > 0 || l.Speed >= 1 {
				used[id] = now
			}
		}
	}
	for _, v := range s.Interfaces {
		if t, ok := used[v.ID]; !ok || now.Sub(t) > linger || len(m.Rows) >= ifaceRows {
			continue
		}
		m.Rows = append(m.Rows, fmt.Sprintf("%s: %s", v.Name, Speed(v.Speed, unit)))
	}
	return m
}

// openDownloads is a left click and "Show downloads": the Downloads window's downloads view.
func openDownloads(e *engine.Engine) { e.OpenWindow("") }

// addFromClipboard adds the links on the clipboard, or says there are none.
func addFromClipboard(e *engine.Engine) {
	links := winutil.Links(winutil.ClipboardText())
	if len(links) == 0 {
		winutil.Notify(winutil.AppName, "No link on the clipboard")
		return
	}
	added := 0
	for _, l := range links {
		if _, err := e.Add(engine.AddRequest{URL: l}); err != nil {
			winutil.Notify("Could not add the link", err.Error())
			continue
		}
		added++
	}
	if added > 0 {
		winutil.Notify(winutil.AppName, fmt.Sprintf("Added %d from the clipboard", added))
	}
}

// openSettings is "Connections & settings": the Downloads window's settings view.
func openSettings(e *engine.Engine) { e.OpenWindow(window.Settings) }

// Speed is bytes per second as text, in the speedUnit setting's unit: bytes
// ("MBps", the default) or bits ("Mbps": Mbit/s = bytes/s * 8 / 1e6). The pages
// have the same rule (fmtSpeed in lib/list.js).
func Speed(bps float64, unit string) string {
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	if unit == engine.SpeedBits {
		units = []string{"bit/s", "kbit/s", "Mbit/s", "Gbit/s"}
		bps *= 8
	}
	if bps < 1 {
		return "0 " + units[0]
	}
	i := 0
	for bps >= 1000 && i < len(units)-1 {
		bps /= 1000
		i++
	}
	return fmt.Sprintf("%.1f %s", bps, units[i])
}

func Duration(sec float64) string {
	if sec < 0 || math.IsInf(sec, 0) || math.IsNaN(sec) {
		return "—"
	}
	d := time.Duration(sec) * time.Second
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
