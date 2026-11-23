package winutil

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// The Windows Connection Manager's policy "Minimize the number of simultaneous
// connections". Unset, Windows minimizes: it drops Wi-Fi while a cable or a
// tethered phone is connected. Home has no Group Policy editor for it, but
// the value works there too.
const (
	wcmKey   = `SOFTWARE\Policies\Microsoft\Windows\WcmSvc\GroupPolicy`
	wcmValue = "fMinimizeConnections"
)

// MinimizesConnections says whether Windows drops Wi-Fi while another
// connection is up: on unless the policy is 0 (keep every connection).
func MinimizesConnections() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, wcmKey, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(wcmValue)
	return err != nil || v != 0
}

// keepAllCommand sets the policy to 0 and restarts the Connection Manager so
// it applies now; -Force restarts the services that depend on it (WLAN
// AutoConfig) as well.
const keepAllCommand = `-NoProfile -NonInteractive -Command "New-Item -Force 'HKLM:\` + wcmKey + `' | Out-Null; ` +
	`New-ItemProperty 'HKLM:\` + wcmKey + `' -Name ` + wcmValue + ` -PropertyType DWord -Value 0 -Force | Out-Null; ` +
	`Restart-Service WcmSvc -Force"`

// KeepAllConnections makes Windows keep every connection up at once, as
// administrator: Windows asks first (ErrElevationDeclined when the user says no).
func KeepAllConnections() error {
	exe := filepath.Join(os.Getenv("SystemRoot"), `System32\WindowsPowerShell\v1.0\powershell.exe`)
	code, err := RunElevated(exe, keepAllCommand, true)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("the Windows setting could not be changed (PowerShell exit code %d)", code)
	}
	return nil
}
