package engine

import (
	"os"
	"testing"
)

// Every bind in these tests stays on loopback: a listen on a real interface
// raises a Windows Firewall prompt, and nobody is there to answer it.
func TestMain(m *testing.M) {
	probeHost = "127.0.0.1"
	os.Exit(m.Run())
}
