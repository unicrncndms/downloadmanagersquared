package winutil

import (
	"testing"
	"time"
)

func TestWaitUpdateWaitsWhileAnInstallHoldsTheGuard(t *testing.T) {
	old := updateGuard
	updateGuard = `Local\downloadmanagersquared-updating-test`
	t.Cleanup(func() { updateGuard = old })

	if WaitUpdate(time.Second) {
		t.Fatal("a guard held with none taken")
	}
	held, release := make(chan struct{}), make(chan struct{})
	go func() {
		r := HoldUpdate()
		close(held)
		<-release
		r()
	}()
	<-held
	start := time.Now()
	if !WaitUpdate(200*time.Millisecond) || time.Since(start) < 200*time.Millisecond {
		t.Fatal("went on at once while the guard was held")
	}
	done := make(chan bool)
	go func() { done <- WaitUpdate(5 * time.Second) }()
	time.Sleep(100 * time.Millisecond)
	close(release)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("the held guard wasn't reported")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitUpdate didn't return after the release")
	}
	if WaitUpdate(time.Second) {
		t.Fatal("the guard is still there after its release")
	}
}
