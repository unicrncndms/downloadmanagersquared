package winutil

import (
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// launch is how the engine opens a page.
type launch int

const (
	launchExe     launch = iota // start the browser's exe with the URL
	launchDefault               // let Windows open the URL in the default browser
)

// browserLaunch picks how to open pages given the exe of the browser the
// extension runs in ("" if unknown). Store apps under \WindowsApps\ can't be
// started by path.
func browserLaunch(exe string) launch {
	if exe == "" || strings.Contains(strings.ToLower(exe), `\windowsapps\`) {
		return launchDefault
	}
	return launchExe
}

// OpenBrowser opens url ("" just starts the browser) in the browser at exe:
// by its path, else with the default browser, else with "start firefox".
func OpenBrowser(exe, url string) error {
	if browserLaunch(exe) == launchExe {
		cmd := exec.Command(exe)
		if url != "" {
			cmd.Args = append(cmd.Args, url)
		}
		if cmd.Start() == nil {
			go cmd.Wait()
			return nil
		}
	}
	if url != "" && ShellOpen(url) == nil {
		return nil
	}
	args := []string{"/c", "start", "", "firefox"}
	if url != "" {
		args = append(args, url)
	}
	return Hidden(exec.Command("cmd.exe", args...)).Start()
}

// ShellOpen opens a URL or file the way Explorer would.
func ShellOpen(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, t, nil, nil, windows.SW_SHOWNORMAL)
}

// ParentExe is the exe of the process that started this one, skipping a
// cmd.exe in between (Chromium starts native hosts through one).
func ParentExe() (string, error) {
	pid := windows.GetCurrentProcessId()
	var exe string
	for range 3 {
		ppid, err := parentPID(pid)
		if err != nil {
			return "", err
		}
		if exe, err = processExe(ppid); err != nil {
			return "", err
		}
		if !strings.EqualFold(filepath.Base(exe), "cmd.exe") {
			break
		}
		pid = ppid
	}
	return exe, nil
}

func parentPID(pid uint32) (uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ProcessID == pid {
			return pe.ParentProcessID, nil
		}
	}
	return 0, err
}

func processExe(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}
