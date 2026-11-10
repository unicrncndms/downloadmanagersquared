package download

import (
	"fmt"
	"path/filepath"
	"strings"
)

// KindTorrent is a torrent's Summary.Kind; an HTTP download's is empty.
const KindTorrent = "torrent"

// Actions are the buttons a download offers.
type Actions struct {
	Pause        bool `json:"pause"`
	Resume       bool `json:"resume"`
	ShowInFolder bool `json:"showInFolder"`
	Remove       bool `json:"remove"`
	Cancel       bool `json:"cancel"` // a torrent waiting for its files to be chosen: Remove under another name
	// Record the landing page's recipe now, while someone is there. Set by the
	// engine, which knows the browsers' recipes, not by Present.
	RecordRecipe bool `json:"recordRecipe"`
}

// Tones a label is shown in. They match the UI's badge classes.
const (
	toneNone = ""
	toneOK   = "ok"
	toneWarn = "warn"
	toneBad  = "bad"
	toneDim  = "dim"
)

var statusText = map[Status]string{
	StatusQueued: "Queued", StatusRunning: "Downloading", StatusPaused: "Paused", StatusVerifying: "Verifying",
	StatusCompleted: "Completed", StatusFailed: "Failed", StatusMetadata: "Finding files", StatusChecking: "Checking files",
	StatusSeeding: "Seeding", StatusChoosing: "Waiting for you",
}

var statusTone = map[Status]string{
	StatusCompleted: toneOK, StatusSeeding: toneOK, StatusFailed: toneBad, StatusPaused: toneDim, StatusQueued: toneDim,
}

var linkText = map[LinkStatus]string{
	LinkPending: "Waiting", LinkProbing: "Checking link", LinkReady: "Ready", LinkResolving: "Getting link in browser",
	LinkNeedsPage: "Needs landing page", LinkRetrying: "Retrying soon", LinkFailed: "Failed", LinkMismatch: "Different file: excluded",
}

var linkTone = map[LinkStatus]string{
	LinkFailed: toneBad, LinkMismatch: toneBad, LinkResolving: toneWarn, LinkNeedsPage: toneWarn, LinkRetrying: toneWarn, LinkReady: toneOK,
}

// Present fills in what the UIs show for a download: its label and tone, each
// link's, the actions it offers, whether it has a partial file and the note on
// its progress line. Every UI shows these as they are (see ADR 0003), so the
// rules for what the engine's state means to a user live only here.
func Present(s Summary) Summary {
	torrent := s.Kind == KindTorrent
	choosing := s.Status == StatusChoosing
	known := s.Probed
	if torrent {
		known = s.Size > 0
	}
	finished := torrent && !s.Finished.IsZero() // complete: what counts now is the upload

	s.Label, s.Tone = statusText[s.Status], statusTone[s.Status]
	if s.Label == "" {
		s.Label = string(s.Status)
	}
	if torrent && s.Status == StatusCompleted {
		s.Label = "Finished" // done seeding
	}
	mismatch := s.hashResult != "" && s.hashResult != "match"
	switch {
	case s.Status == StatusCompleted && s.checking:
		s.Label, s.Tone = statusText[StatusVerifying], toneNone
	case s.Status == StatusCompleted && mismatch:
		s.Label, s.Tone, s.Error = "Checksum mismatch", toneBad, s.hashResult
	}

	switch s.Status {
	case StatusRunning, StatusVerifying, StatusQueued, StatusMetadata, StatusChecking, StatusSeeding:
		s.Actions.Pause = true
	case StatusChoosing, StatusCompleted:
	default:
		s.Actions.Resume = true
	}
	s.Actions.ShowInFolder = known && !choosing
	s.Actions.Remove, s.Actions.Cancel = !choosing, choosing
	s.PartialFile = s.Status != StatusCompleted && !finished

	switch {
	case choosing && len(s.Files) == 0:
		s.Note = "Not in the swarm yet: finding the files needs peers"
	case choosing:
		s.Note = ""
	case finished:
		s.Note = "complete"
	case s.Status == StatusCompleted:
		s.Note = ""
		if s.hashResult == "match" && !s.checking {
			s.Note = matched(s.SHA256 != "", s.MD5 != "")
			if s.serverSHA || s.serverMD {
				s.Note = strings.Replace(matched(s.serverSHA, s.serverMD), " match", " from the server match", 1)
			}
		}
	case !known && torrent:
		s.Note = "Asking peers for the torrent's files…"
	case !known:
		s.Note = "Checking the link…"
	case !torrent && !s.RangeOK:
		s.Note = "main connection only"
	}

	if f := s.Folder; f != nil && f.On {
		switch {
		case f.Set != nil && f.Set.Note != "":
			s.Note = f.Set.Note
		case s.Status == StatusCompleted && f.Set != nil && !f.Set.Finished:
			s.Note = "kept until its set is extracted"
		case s.Status == StatusCompleted && f.Out != "":
			s.Note = "Extracted to " + filepath.Base(f.Out)
		case s.Status == StatusCompleted:
		case f.Ask != "":
			s.Note = joinNote(s.Note, "needs the archive's password")
		case f.Into == "":
			s.Note = joinNote(s.Note, "choose a folder to extract into")
		case f.Of > 0:
			s.Note = joinNote(s.Note, fmt.Sprintf("%s of %s files extracted", thousands(f.Files), thousands(f.Of)))
		}
		switch {
		case s.Status == StatusCompleted:
		case f.MovingNow:
			s.Label, s.Tone = "Moving…", toneNone
		case f.Moving != "" && s.Status != StatusRunning:
			s.Note = joinNote(s.Note, "moves to "+f.Moving+" when it resumes")
		}
		if f.Refetch > 0 && s.Status != StatusCompleted {
			s.Folder.MoveWarning = "Moving now re-downloads about " + human(f.Refetch)
		}
	}

	links := make(map[string]LinkSummary, len(s.Links))
	for id, l := range s.Links {
		links[id] = presentLink(l, s)
	}
	s.Links = links
	return s
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + " · " + b
}

// matched is the note for checksums that match the finished file.
func matched(sha, md bool) string {
	switch {
	case sha && md:
		return "SHA-256 and MD5 match"
	case sha:
		return "SHA-256 matches"
	case md:
		return "MD5 matches"
	}
	return ""
}

func presentLink(l LinkSummary, s Summary) LinkSummary {
	l.Label, l.Tone = linkText[l.Status], linkTone[l.Status]
	if l.Label == "" {
		l.Label = string(l.Status)
	}
	switch {
	case s.Kind == KindTorrent && l.Status == LinkReady:
		switch s.Status {
		case StatusSeeding:
			l.Label = "Seeding"
		case StatusRunning:
			l.Label = "Downloading"
		default:
			l.Label = "Connected"
		}
	case l.busy && (l.Status == LinkReady || l.Status == LinkRetrying): // Backoff
		l.Label, l.Tone = fmt.Sprintf("Server busy · %d of %d connections", l.allowed, l.maxConns), toneWarn
	case l.Status == LinkReady && s.Status == StatusRunning && l.Conns > 0:
		l.Label = "Downloading"
	}
	l.CanRetry = l.Status == LinkRetrying || l.Status == LinkFailed || l.Status == LinkMismatch
	return l
}
