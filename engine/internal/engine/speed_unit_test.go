package engine

import "testing"

func TestSpeedUnitSettingDefaultsToBytesAndRejectsOtherValues(t *testing.T) {
	e, err := open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	if got := e.SpeedUnit(); got != SpeedBytes {
		t.Fatalf("default speedUnit %q, want %q", got, SpeedBytes)
	}
	bits, junk := SpeedBits, "furlongs"
	if s := e.setSettings(settingsPatch{SpeedUnit: &bits, DownloadLimit: new(int64)}); s.SpeedUnit != SpeedBits || e.SpeedUnit() != SpeedBits {
		t.Fatalf("after setting %q: %q", SpeedBits, s.SpeedUnit)
	}
	if s := e.setSettings(settingsPatch{SpeedUnit: &junk}); s.SpeedUnit != SpeedBytes {
		t.Fatalf("an unknown unit gave %q, want %q", s.SpeedUnit, SpeedBytes)
	}
}
