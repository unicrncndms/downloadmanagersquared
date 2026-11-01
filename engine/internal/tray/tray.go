// Package tray is the engine's notification-area icon.
package tray

import (
	"fmt"
	"math"
	"strings"
	"time"

	"fyne.io/systray"

	"downloadmanagersquared/engine/internal/engine"
	"downloadmanagersquared/engine/internal/winutil"
)

const ifaceRows = 6

// Run blocks until the user quits from the tray menu.
func Run(e *engine.Engine) {
	systray.Run(func() { onReady(e) }, e.Shutdown)
}

func onReady(e *engine.Engine) {
	systray.SetIcon(Icon())
	systray.SetTooltip("downloadmanagersquared")

	summary := systray.AddMenuItem("Idle", "")
	summary.Disable()
	var rows []*systray.MenuItem
	for range ifaceRows {
		r := systray.AddMenuItem("", "")
		r.Disable()
		r.Hide()
		rows = append(rows, r)
	}
	systray.AddSeparator()
	open := systray.AddMenuItem("Open downloads", "Show the downloads in Firefox")
	pause := systray.AddMenuItem("Pause all", "")
	resume := systray.AddMenuItem("Resume all", "")
	settings := systray.AddMenuItem("Connections & settings", "")
	systray.AddSeparator()
	autostart := systray.AddMenuItemCheckbox("Start with Windows", "", winutil.Autostart())
	quit := systray.AddMenuItem("Quit", "Stop all downloads and exit")

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				e.OpenManager("")
			case <-settings.ClickedCh:
				e.OpenManager("#settings")
			case <-pause.ClickedCh:
				go e.PauseAll()
			case <-resume.ClickedCh:
				go e.ResumeAll()
			case <-autostart.ClickedCh:
				on := !autostart.Checked()
				if winutil.SetAutostart(on) == nil {
					if on {
						autostart.Check()
					} else {
						autostart.Uncheck()
					}
				}
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	go func() {
		for range time.Tick(time.Second) {
			s := e.Snapshot()
			head := "Idle"
			if s.Active > 0 {
				head = fmt.Sprintf("%s  ·  %s left", Speed(s.Speed), Duration(s.ETA))
			}
			summary.SetTitle(head)

			tip := []string{"DMS²  " + head}
			i := 0
			for _, v := range s.Interfaces {
				if !v.Present || !v.Enabled || i >= ifaceRows {
					continue
				}
				line := fmt.Sprintf("%s: %s", v.Name, Speed(v.Speed))
				rows[i].SetTitle(line)
				rows[i].Show()
				tip = append(tip, line)
				i++
			}
			for ; i < ifaceRows; i++ {
				rows[i].Hide()
			}
			// Windows cuts tray tooltips at 127 characters.
			t := strings.Join(tip, "\n")
			if len(t) > 127 {
				t = t[:127]
			}
			systray.SetTooltip(t)
		}
	}()
}

func Speed(bps float64) string {
	if bps < 1 {
		return "0 B/s"
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
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
