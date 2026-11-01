package download

import (
	"testing"
	"time"
)

var start = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestLinkGivesUpAfterFiveHTTPErrors(t *testing.T) {
	l := &Link{}
	l.probing()
	now := start
	for i := 1; i <= 4; i++ {
		l.httpError(now, "HTTP 500")
		if l.Status != LinkRetrying {
			t.Fatalf("after %d errors: %s, want retrying", i, l.Status)
		}
		now = now.Add(61 * time.Second)
		l.wake(now)
		if l.Status != LinkPending {
			t.Fatalf("after %d errors and a minute: %s, want pending", i, l.Status)
		}
		l.probing()
	}
	l.httpError(now, "HTTP 500")
	if l.Status != LinkFailed {
		t.Fatalf("after 5 errors: %s, want failed", l.Status)
	}
}

func TestUnreachableInterfaceKeepsRetrying(t *testing.T) {
	l := &Link{}
	l.probing()
	now := start
	for i := 1; i <= 20; i++ {
		l.unreachable(now, "no route")
		if l.Status != LinkRetrying {
			t.Fatalf("after %d tries: %s, want retrying", i, l.Status)
		}
		l.wake(now.Add(29 * time.Second))
		if l.Status != LinkRetrying {
			t.Fatal("retried before the 30s pause was over")
		}
		now = now.Add(30 * time.Second)
		l.wake(now)
		l.probing()
	}
}

func TestResumingRetriesFailedLinksOnlyAfterGettingStuck(t *testing.T) {
	links := func() (retrying, failed, mismatch, needsPage *Link) {
		retrying = &Link{}
		retrying.probing()
		retrying.httpError(start, "HTTP 503")
		return retrying, &Link{Status: LinkFailed, Error: "gave up after 3 new links", Resolves: 3},
			&Link{Status: LinkMismatch, Error: "different file size"}, &Link{Status: LinkNeedsPage}
	}

	retrying, failed, mismatch, needsPage := links()
	for _, l := range []*Link{retrying, failed, mismatch, needsPage} {
		l.resume(false)
	}
	if retrying.Status != LinkPending || failed.Status != LinkFailed {
		t.Errorf("plain resume: retrying %s, failed %s; want pending, failed", retrying.Status, failed.Status)
	}

	retrying, failed, mismatch, needsPage = links()
	for _, l := range []*Link{retrying, failed, mismatch, needsPage} {
		l.resume(true)
	}
	if retrying.Status != LinkPending || failed.Status != LinkPending || failed.Resolves != 0 {
		t.Errorf("resume after getting stuck: retrying %s, failed %s (resolves %d); want both pending afresh", retrying.Status, failed.Status, failed.Resolves)
	}
	if mismatch.Status != LinkMismatch || needsPage.Status != LinkNeedsPage {
		t.Errorf("mismatch %s, needs page %s: resuming shouldn't touch them", mismatch.Status, needsPage.Status)
	}
}

func TestStuckLinks(t *testing.T) {
	for st, want := range map[LinkStatus]bool{
		LinkPending: false, LinkProbing: false, LinkReady: false, LinkResolving: false, LinkRetrying: false,
		LinkNeedsPage: true, LinkFailed: true, LinkMismatch: true,
	} {
		if got := (&Link{Status: st}).stuck(); got != want {
			t.Errorf("%s: stuck %v, want %v", st, got, want)
		}
	}
}

func TestRestoredLinkIsDueAtOnce(t *testing.T) {
	l := &Link{}
	l.probing()
	l.unreachable(start, "no route")
	l.restored()
	if l.Status != LinkPending {
		t.Fatalf("restored retrying link is %s, want pending", l.Status)
	}
}
