package winutil

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestProcessUserOfThisProcessIsTheCurrentUser(t *testing.T) {
	me, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ProcessUser(windows.GetCurrentProcessId())
	if err != nil {
		t.Fatal(err)
	}
	if got != me {
		t.Fatalf("ProcessUser = %s, want %s", got, me)
	}
	if _, err := ProcessUser(0xfffffff0); err == nil {
		t.Fatal("a process that doesn't exist has an owner")
	}
}
