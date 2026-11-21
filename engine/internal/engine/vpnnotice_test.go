package engine

import (
	"testing"

	"downloadmanagersquared/engine/internal/download"
)

// When the VPN drops under VPN only, the user hears it once (balloon, and
// vpnDown for the pages' banner), and once more when downloads go on; not at
// engine start while the tunnels are still connecting.
func TestTheVPNDropIsToldOnceAndTheReturnToo(t *testing.T) {
	dir := t.TempDir()
	e, err := open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown()
	var said []string
	was := notify
	notify = func(title, _ string, _ func()) { said = append(said, title) }
	defer func() { notify = was }()

	id, err := e.Add(AddRequest{URL: "https://example.com/big.iso", VPN: "vpn"})
	if err != nil {
		t.Fatal(err)
	}
	e.schedule() // no tunnel: it waits for one
	j, _ := e.queue.Get(id)
	if !j.WaitingForVPN() {
		t.Fatalf("not waiting for the VPN: %+v", j.Summary())
	}
	if len(said) != 0 || e.snapshotVPNDown() {
		t.Fatalf("told at start, before any tunnel was up: %v", said)
	}
	e.noticeVPN(true)  // a tunnel came up (the download hasn't resumed yet)
	e.noticeVPN(false) // and dropped again
	e.noticeVPN(false)
	if len(said) != 1 || said[0] != "VPN disconnected" || !e.snapshotVPNDown() {
		t.Fatalf("after the drop: %v, vpnDown %v", said, e.snapshotVPNDown())
	}
	j.Resume()
	defer j.Stop(download.StatusPaused)
	e.noticeVPN(true)
	e.noticeVPN(true)
	if len(said) != 2 || said[1] != "VPN connected again" || e.snapshotVPNDown() {
		t.Fatalf("after the return: %v, vpnDown %v", said, e.snapshotVPNDown())
	}
}

func (e *Engine) snapshotVPNDown() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.vpnDown
}
