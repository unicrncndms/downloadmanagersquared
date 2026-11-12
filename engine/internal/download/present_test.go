package download

import (
	"testing"
	"time"
)

func TestPresentLabelsDownloads(t *testing.T) {
	done := time.Date(2026, 11, 3, 18, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		in          Summary
		label, tone string
		note        string
		actions     Actions
		partialFile bool
	}{
		{"running", Summary{Status: StatusRunning, Probed: true, RangeOK: true, Size: 10},
			"Downloading", "", "", Actions{Pause: true, ShowInFolder: true, Remove: true}, true},
		{"not probed yet", Summary{Status: StatusQueued},
			"Queued", "dim", "Waiting in the queue", Actions{Pause: true, Remove: true}, true},
		{"no ranges", Summary{Status: StatusRunning, Probed: true, Size: 10},
			"Downloading", "", "main connection only", Actions{Pause: true, ShowInFolder: true, Remove: true}, true},
		{"paused", Summary{Status: StatusPaused, Probed: true, RangeOK: true},
			"Paused", "dim", "", Actions{Resume: true, ShowInFolder: true, Remove: true}, true},
		{"failed", Summary{Status: StatusFailed, Probed: true, RangeOK: true},
			"Failed", "bad", "", Actions{Resume: true, ShowInFolder: true, Remove: true}, true},
		{"completed", Summary{Status: StatusCompleted, Probed: true, RangeOK: true},
			"Completed", "ok", "", Actions{ShowInFolder: true, Remove: true}, false},
		{"torrent learning files", Summary{Kind: KindTorrent, Status: StatusMetadata},
			"Finding files", "", "Asking peers for the torrent's files…", Actions{Pause: true, Remove: true}, true},
		{"torrent choosing before files", Summary{Kind: KindTorrent, Status: StatusChoosing},
			"Waiting for you", "", "Not in the swarm yet: finding the files needs peers", Actions{Cancel: true}, true},
		{"torrent choosing files", Summary{Kind: KindTorrent, Status: StatusChoosing, Size: 10, Files: []FileSummary{{Size: 10}}},
			"Waiting for you", "", "", Actions{Cancel: true}, true},
		{"torrent seeding", Summary{Kind: KindTorrent, Status: StatusSeeding, Size: 10, Done: 10, Finished: done},
			"Seeding", "ok", "complete", Actions{Pause: true, ShowInFolder: true, Remove: true}, false},
		{"torrent done seeding", Summary{Kind: KindTorrent, Status: StatusCompleted, Size: 10, Done: 10, Finished: done},
			"Finished", "ok", "complete", Actions{ShowInFolder: true, Remove: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Present(c.in)
			if s.Label != c.label || s.Tone != c.tone || s.Note != c.note {
				t.Errorf("got label %q tone %q note %q, want %q %q %q", s.Label, s.Tone, s.Note, c.label, c.tone, c.note)
			}
			if s.Actions != c.actions {
				t.Errorf("actions %+v, want %+v", s.Actions, c.actions)
			}
			if s.PartialFile != c.partialFile {
				t.Errorf("partialFile %v, want %v", s.PartialFile, c.partialFile)
			}
		})
	}
}

func TestPresentLabelsLinks(t *testing.T) {
	cases := []struct {
		name        string
		kind        string
		status      Status
		link        LinkSummary
		label, tone string
		canRetry    bool
	}{
		{"fetching", "", StatusRunning, LinkSummary{Status: LinkReady, Conns: 4}, "Downloading", "ok", false},
		{"ready but idle", "", StatusPaused, LinkSummary{Status: LinkReady}, "Ready", "ok", false},
		{"backing off", "", StatusRunning, LinkSummary{Status: LinkReady, Conns: 2, busy: true, allowed: 2, maxConns: 4},
			"Server busy · 2 of 4 connections", "warn", false},
		{"backing off while retrying", "", StatusRunning, LinkSummary{Status: LinkRetrying, busy: true, allowed: 0, maxConns: 4},
			"Server busy · 0 of 4 connections", "warn", true},
		{"retrying", "", StatusRunning, LinkSummary{Status: LinkRetrying}, "Retrying soon", "warn", true},
		{"failed", "", StatusRunning, LinkSummary{Status: LinkFailed}, "Failed", "bad", true},
		{"mismatch", "", StatusRunning, LinkSummary{Status: LinkMismatch}, "Different file: excluded", "bad", true},
		{"needs landing page", "", StatusRunning, LinkSummary{Status: LinkNeedsPage}, "Needs landing page", "warn", false},
		{"resolving", "", StatusRunning, LinkSummary{Status: LinkResolving}, "Getting link in browser", "warn", false},
		{"probing", "", StatusRunning, LinkSummary{Status: LinkProbing}, "Checking link", "", false},
		{"torrent downloading", KindTorrent, StatusRunning, LinkSummary{Status: LinkReady, Conns: 3}, "Downloading", "ok", false},
		{"torrent seeding", KindTorrent, StatusSeeding, LinkSummary{Status: LinkReady, Conns: 3}, "Seeding", "ok", false},
		{"torrent checking", KindTorrent, StatusChecking, LinkSummary{Status: LinkReady, Conns: 3}, "Connected", "ok", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Present(Summary{Kind: c.kind, Status: c.status, Links: map[string]LinkSummary{"eth": c.link}})
			l := s.Links["eth"]
			if l.Label != c.label || l.Tone != c.tone || l.CanRetry != c.canRetry {
				t.Errorf("got %q %q retry %v, want %q %q retry %v", l.Label, l.Tone, l.CanRetry, c.label, c.tone, c.canRetry)
			}
		})
	}
}

func TestPresentSection(t *testing.T) {
	retry := map[string]LinkSummary{"eth": {Status: LinkFailed}}
	on := func(f FolderSummary) *FolderSummary { f.On = true; return &f }
	cases := []struct {
		name string
		in   Summary
		want string
	}{
		{"running", Summary{Status: StatusRunning}, SectionActive},
		{"verifying", Summary{Status: StatusVerifying}, SectionActive},
		{"finding files", Summary{Kind: KindTorrent, Status: StatusMetadata}, SectionActive},
		{"checking files", Summary{Kind: KindTorrent, Status: StatusChecking}, SectionActive},
		{"seeding", Summary{Kind: KindTorrent, Status: StatusSeeding}, SectionSeeding},
		{"queued", Summary{Status: StatusQueued}, SectionQueued},
		{"paused", Summary{Status: StatusPaused}, SectionPaused},
		{"paused with an error", Summary{Status: StatusPaused, Error: "disk full"}, SectionNeeds},
		{"paused for the VPN", Summary{Status: StatusPaused, Error: "no tunnel", WaitVPN: true}, SectionPaused},
		{"torrent paused for the VPN", Summary{Kind: KindTorrent, Status: StatusPaused, Error: "no tunnel", WaitVPN: true}, SectionPaused},
		{"failed", Summary{Status: StatusFailed}, SectionNeeds},
		{"torrent choosing", Summary{Kind: KindTorrent, Status: StatusChoosing}, SectionNeeds},
		{"link to retry", Summary{Status: StatusRunning, Links: retry}, SectionNeeds},
		{"completed", Summary{Status: StatusCompleted}, SectionDone},
		{"torrent done seeding", Summary{Kind: KindTorrent, Status: StatusCompleted}, SectionDone},
		{"folder without a folder chosen", Summary{Status: StatusRunning, Folder: on(FolderSummary{})}, SectionNeeds},
		{"folder chosen", Summary{Status: StatusRunning, Folder: on(FolderSummary{Into: "x"})}, SectionActive},
		{"folder off", Summary{Status: StatusRunning, Folder: &FolderSummary{}}, SectionActive},
		{"torrent ignores the folder", Summary{Kind: KindTorrent, Status: StatusRunning, Folder: on(FolderSummary{})}, SectionActive},
		{"completed folder without a folder chosen", Summary{Status: StatusCompleted, Folder: on(FolderSummary{})}, SectionDone},
		{"archive password wanted", Summary{Status: StatusRunning, Folder: on(FolderSummary{Into: "x", Ask: AskPassword})}, SectionNeeds},
		{"password wanted by a fetched part", Summary{Status: StatusCompleted, Folder: on(FolderSummary{Into: "x", Ask: AskWrong, Set: &SetSummary{}})}, SectionNeeds},
		{"password wanted by a finished download", Summary{Status: StatusCompleted, Folder: on(FolderSummary{Into: "x", Ask: AskWrong})}, SectionDone},
		{"a part missing", Summary{Status: StatusRunning, Folder: on(FolderSummary{Into: "x", Set: &SetSummary{Missing: []int{2}}})}, SectionNeeds},
		{"a part fetched while its set extracts", Summary{Status: StatusCompleted, Folder: on(FolderSummary{Into: "x", Set: &SetSummary{}})}, SectionActive},
		{"a part of an extracted set", Summary{Status: StatusCompleted, Folder: on(FolderSummary{Into: "x", Set: &SetSummary{Finished: true}})}, SectionDone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Present(c.in).Section; got != c.want {
				t.Errorf("section %q, want %q", got, c.want)
			}
		})
	}
}

// A label that only restates the status is plain, so the row leaves it off;
// one that says more (Moving…, Failed, a checksum mismatch) is not.
func TestPresentPlain(t *testing.T) {
	moving := &FolderSummary{On: true, Into: "x", MovingNow: true}
	cases := []struct {
		name string
		in   Summary
		want bool
	}{
		{"downloading", Summary{Status: StatusRunning}, true},
		{"queued", Summary{Status: StatusQueued}, true},
		{"paused", Summary{Status: StatusPaused}, true},
		{"seeding", Summary{Kind: KindTorrent, Status: StatusSeeding}, true},
		{"moving", Summary{Status: StatusRunning, Folder: moving}, false},
		{"failed", Summary{Status: StatusFailed}, false},
		{"completed", Summary{Status: StatusCompleted}, false},
	}
	for _, c := range cases {
		if got := Present(c.in).Plain; got != c.want {
			t.Errorf("%s: plain %v, want %v", c.name, got, c.want)
		}
	}
}
