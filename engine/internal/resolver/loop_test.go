package resolver

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// rig is a Resolver over a fake browser and a fake clock.
type rig struct {
	t      *testing.T
	r      *Resolver
	f      *fakeBrowser
	d      *Driver
	ev     Events
	starts int

	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	n      int // pages opened
	log    []string
}

type fakeTimer struct {
	at   time.Time
	f    func()
	dead bool
}

type fakeHandle struct{ done chan struct{} }

func (h *fakeHandle) Done() <-chan struct{} { return h.done }
func (h *fakeHandle) Close() {
	select {
	case <-h.done:
	default:
		close(h.done)
	}
}

func newRig(t *testing.T) *rig {
	g := &rig{t: t, now: time.Unix(1_700_000_000, 0)}
	g.f = &fakeBrowser{}
	ctxN := 0
	g.f.reply = func(m string, p map[string]any) (string, error) {
		switch m {
		case "Target.createBrowserContext":
			ctxN++
			return fmt.Sprintf(`{"browserContextId":"ctx%d"}`, ctxN), nil
		case "Target.createTarget":
			g.mu.Lock()
			g.n++
			id := fmt.Sprintf("tab%d", g.n)
			g.mu.Unlock()
			// The browser attaches it at once, as a real one does.
			g.d.event(ev("Target.attachedToTarget", "", map[string]any{"sessionId": "s-" + id, "targetInfo": map[string]any{"targetId": id, "type": "page", "browserContextId": p["browserContextId"]}}))
			return `{"targetId":"` + id + `"}`, nil
		case "Page.getFrameTree":
			return `{"frameTree":{"frame":{"id":"F"}}}`, nil
		case "Page.createIsolatedWorld":
			return `{"executionContextId":1}`, nil
		case "Browser.getWindowForTarget":
			return `{"windowId":1}`, nil
		}
		return "", nil
	}
	recipes, err := OpenRecipes(filepath.Join(t.TempDir(), "recipes.json"))
	if err != nil {
		t.Fatal(err)
	}
	g.r = &Resolver{
		Recipes: recipes,
		Idle:    time.Minute,
		Hooks: Hooks{
			Resolved: func(id, iface, url, cookie, ref string) {
				g.say("resolved %s %s %s cookie=%s referer=%s", id, iface, url, cookie, ref)
			},
			Failed:        func(id, iface, why string) { g.say("failed %s %s: %s", id, iface, why) },
			LearnEnded:    func(id, iface string) { g.say("learnEnded %s %s", id, iface) },
			SetPageURL:    func(id, u string) { g.say("pageURL %s %s", id, u) },
			LearnStepBack: func(id, u string) { g.say("stepBack %s %s", id, u) },
			RecipeHosts:   func(h []string) { g.say("hosts %s", strings.Join(h, ",")) },
			Notify:        func(title, _ string) { g.say("notify %s", title) },
		},
		now: func() time.Time { g.mu.Lock(); defer g.mu.Unlock(); return g.now },
		after: func(d time.Duration, f func()) func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			ft := &fakeTimer{at: g.now.Add(d), f: f}
			g.timers = append(g.timers, ft)
			return func() { g.mu.Lock(); ft.dead = true; g.mu.Unlock() }
		},
	}
	g.r.Start = func(ev Events) (*Driver, Browser, error) {
		g.starts++
		g.ev = ev
		g.d = newDriver(ev)
		g.d.b = g.f
		return g.d, &fakeHandle{done: make(chan struct{})}, nil
	}
	was := pageScript
	t.Cleanup(func() { pageScript = was })
	pageScript = func() (string, error) { return prelude, nil }
	return g
}

func (g *rig) say(format string, a ...any) {
	g.mu.Lock()
	g.log = append(g.log, fmt.Sprintf(format, a...))
	g.mu.Unlock()
}

// said returns and clears what the hooks said.
func (g *rig) said() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := strings.Join(g.log, "\n")
	g.log = nil
	return s
}

// advance moves the clock, firing what falls due, in order.
func (g *rig) advance(d time.Duration) {
	g.mu.Lock()
	end := g.now.Add(d)
	g.mu.Unlock()
	for {
		g.mu.Lock()
		sort.SliceStable(g.timers, func(i, j int) bool { return g.timers[i].at.Before(g.timers[j].at) })
		var next *fakeTimer
		for _, ft := range g.timers {
			if !ft.dead && !ft.at.After(end) {
				next = ft
				break
			}
		}
		if next == nil {
			g.now = end
			g.mu.Unlock()
			return
		}
		next.dead = true
		if next.at.After(g.now) {
			g.now = next.at
		}
		g.mu.Unlock()
		next.f()
	}
}

// load attaches tab (once) and fires its navigation and load to url.
func (g *rig) load(tab, ctx, url string) {
	if g.d.session(tab) == "" {
		g.d.event(ev("Target.attachedToTarget", "", map[string]any{"sessionId": "s-" + tab, "targetInfo": map[string]any{"targetId": tab, "type": "page", "browserContextId": ctx}}))
	}
	g.d.event(ev("Page.frameNavigated", "s-"+tab, map[string]any{"frame": map[string]any{"id": "F", "url": url}}))
	g.d.event(ev("Page.loadEventFired", "s-"+tab, map[string]any{}))
	g.sync()
}

func (g *rig) msg(tab string, m map[string]any) {
	m["resolver"] = true
	b, _ := json.Marshal(m)
	g.ev.Message(tab, string(b))
	g.sync()
}

// sent is the last message the page script got, "" if none.
func (g *rig) sent() map[string]any {
	e := g.f.last("Runtime.evaluate")
	if e == nil {
		return nil
	}
	js, _ := e.params["expression"].(string)
	if !strings.HasPrefix(js, "__dmsDeliver(") {
		return nil
	}
	var m map[string]any
	json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(js, "__dmsDeliver("), ")")), &m)
	return m
}

func (g *rig) count(method string) int {
	g.f.mu.Lock()
	defer g.f.mu.Unlock()
	n := 0
	for _, c := range g.f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

func (g *rig) want(t *testing.T, want string) {
	t.Helper()
	if got := g.said(); got != want {
		t.Fatalf("hooks said:\n%s\nwant:\n%s", got, want)
	}
}

func req(id, iface, page string) Request {
	return Request{ID: id, Iface: iface, IfaceName: "Wi-Fi", PageURL: page, SocksPort: 4001}
}

func TestAFirstVisitRecordsTheRecipeAndReportsTheLink(t *testing.T) {
	g := newRig(t)
	g.r.Request(Request{ID: "d1", Iface: "wifi", IfaceName: "Wi-Fi", PageURL: "https://host.example/file/1", SocksPort: 4001,
		Cookies: []Cookie{{Name: "login", Value: "me", Domain: "host.example"}}, Pages: []string{"https://host.example/", "https://host.example/file/1"}})
	if c := g.f.last("Target.createTarget"); c.params["windowState"] != "normal" || c.params["url"] != "about:blank" {
		t.Fatalf("record opens shown, blank until attached: %v", c.params)
	}
	if c := g.f.last("Page.navigate"); c == nil || c.session != "s-tab1" || c.params["url"] != "https://host.example/file/1" {
		t.Fatal("the attached page was not sent to the landing page")
	}
	if c := g.f.last("Storage.setCookies"); c == nil || c.params["browserContextId"] != "ctx1" {
		t.Fatal("the user's login was not copied in")
	}
	g.want(t, "notify Show DMS² how to get the link")
	g.load("tab1", "ctx1", "https://host.example/file/1")
	if m := g.sent(); m["cmd"] != "record" || m["prev"] != "https://host.example/" || m["n"] != float64(1) {
		t.Fatalf("record message %v", m)
	}
	g.advance(3 * time.Second)
	g.msg("tab1", map[string]any{"type": "recorded", "step": map[string]any{"tag": "button", "text": "Free download"}})
	g.advance(30 * time.Second)
	g.msg("tab1", map[string]any{"type": "recorded", "step": map[string]any{"tag": "a", "id": "go"}})
	g.advance(10 * time.Second)
	g.ev.Caught("tab1", Catch{URL: "https://cdn.example/f.zip", Cookie: "s=1"})
	g.sync()
	g.want(t, "hosts host.example\nstepBack d1 https://host.example/file/1\nresolved d1 wifi https://cdn.example/f.zip cookie=s=1 referer=https://host.example/file/1")
	rec := g.r.Recipes.Get("host.example")
	if rec == nil || len(rec.Steps) != 2 || rec.Steps[0].Wait != 3000 || rec.Steps[1].Wait != 30000 || rec.DownloadWait != 10000 {
		t.Fatalf("recipe %+v", rec)
	}
	if g.f.last("Target.closeTarget") == nil || g.f.last("Target.disposeBrowserContext") == nil {
		t.Fatal("the window and the context were left")
	}
	g.advance(2 * time.Minute)
	if g.d != nil && g.r.b != nil {
		t.Fatal("the browser stayed after Idle")
	}
}

func recipeFor(g *rig, host string, waits ...int64) {
	rec := &Recipe{DownloadWait: 5000}
	for i, w := range waits {
		rec.Steps = append(rec.Steps, Step{Tag: "button", ID: fmt.Sprintf("b%d", i), Wait: w})
	}
	g.r.Recipes.Set(host, rec)
}

func TestAReplayClicksStepByStepMinimizedAndKeepsTheWaitsItSaw(t *testing.T) {
	g := newRig(t)
	recipeFor(g, "host.example", 1000, 2000)
	g.r.Request(req("d1", "wifi", "https://host.example/f"))
	if c := g.f.last("Target.createTarget"); c.params["windowState"] != "minimized" {
		t.Fatalf("replay opens minimized: %v", c.params)
	}
	g.load("tab1", "ctx1", "https://host.example/f")
	if m := g.sent(); m["cmd"] != "replay" || m["index"] != float64(0) || m["budget"] != float64(17000) {
		t.Fatalf("first step %v", m)
	}
	g.advance(1500 * time.Millisecond)
	g.msg("tab1", map[string]any{"type": "clicked", "index": 0})
	g.load("tab1", "ctx1", "https://host.example/f/2") // the click navigated
	if m := g.sent(); m["index"] != float64(1) {
		t.Fatalf("second step after the load: %v", m)
	}
	g.advance(2500 * time.Millisecond)
	g.msg("tab1", map[string]any{"type": "clicked", "index": 1})
	g.msg("tab1", map[string]any{"type": "effect", "kind": "nextStep", "index": 1})
	g.advance(4 * time.Second)
	g.ev.Caught("tab1", Catch{URL: "https://cdn/f.zip"})
	g.sync()
	g.want(t, "resolved d1 wifi https://cdn/f.zip cookie= referer=https://host.example/f/2")
	rec := g.r.Recipes.Get("host.example")
	if rec.Steps[0].Wait != 1500 || rec.Steps[1].Wait != 2500 || rec.DownloadWait != 4000 {
		t.Fatalf("measured waits %+v", rec)
	}
}

func TestAStepOutOfTimeGetsTheWindowShownOnceThenAsksForHelp(t *testing.T) {
	g := newRig(t)
	recipeFor(g, "host.example", 1000)
	g.r.Request(req("d1", "wifi", "https://host.example/f"))
	g.load("tab1", "ctx1", "https://host.example/f")
	activations := g.count("Target.activateTarget")
	g.advance(18*time.Second + time.Millisecond) // budget(1000) + 1 s
	if g.count("Browser.setWindowBounds") == 0 || g.count("Target.activateTarget") != activations {
		t.Fatal("no second chance shown without focus")
	}
	if m := g.sent(); m["cmd"] != "replay" {
		t.Fatalf("the step was not sent again: %v", m)
	}
	g.advance(18*time.Second + time.Millisecond)
	if m := g.sent(); m["cmd"] != "record" {
		t.Fatalf("no switch to record: %v", m)
	}
	g.want(t, "notify DMS² needs a hand")
}

func TestACaptchaShowsTheWindowAndHoldsTheBudgetUntilSolved(t *testing.T) {
	g := newRig(t)
	recipeFor(g, "host.example", 1000)
	g.r.Request(req("d1", "wifi", "https://host.example/f"))
	g.load("tab1", "ctx1", "https://host.example/f")
	g.msg("tab1", map[string]any{"type": "captcha"})
	g.want(t, "notify DMS² needs you to solve a captcha")
	if g.count("Target.activateTarget") != 1 {
		t.Fatal("the captcha window was not brought to front")
	}
	g.advance(3 * time.Minute) // far past the step's budget
	if m := g.sent(); m["cmd"] != "replay" {
		t.Fatalf("the budget ran during the captcha: %v", m)
	}
	minimizes := g.count("Browser.setWindowBounds")
	g.msg("tab1", map[string]any{"type": "captchaDone"})
	if g.count("Browser.setWindowBounds") != minimizes+1 {
		t.Fatal("not minimized after the captcha")
	}
	if g.said() != "" {
		t.Fatal("something ended during the captcha")
	}
	g.advance(4 * time.Minute) // past the 5 minutes since the captcha came
	if s := g.said(); strings.Contains(s, "captcha wasn't solved") {
		t.Fatalf("the captcha idle timer outlived the captcha: %s", s)
	}
}

func TestAClickThatOpensAnAdTabClosesItAndOneOnTheSameSiteIsFollowed(t *testing.T) {
	g := newRig(t)
	recipeFor(g, "host.example", 1000, 1000)
	g.r.Request(req("d1", "wifi", "https://host.example/f"))
	g.load("tab1", "ctx1", "https://host.example/f")
	g.msg("tab1", map[string]any{"type": "clicked", "index": 0})
	g.ev.Opened("ad", "tab1", "https://ads.example.net/x")
	g.sync()
	g.advance(popupGrace + time.Millisecond)
	if c := g.f.last("Target.closeTarget"); c == nil || c.params["targetId"] != "ad" {
		t.Fatal("the ad tab stayed")
	}
	g.ev.Closed("ad")
	g.sync()
	g.ev.Opened("tab2", "tab1", "https://dl.host.example/next")
	g.sync()
	g.load("tab2", "ctx1", "https://dl.host.example/next")
	if m := g.sent(); m["index"] != float64(1) {
		t.Fatalf("the job did not follow the same-site tab: %v", m)
	}
	g.ev.Caught("tab2", Catch{URL: "https://cdn/f"})
	g.sync()
	if s := g.said(); !strings.HasPrefix(s, "resolved d1 wifi https://cdn/f") {
		t.Fatalf("hooks %s", s)
	}
}

func TestAClickWithNoEffectIsRetriedThenTheRecipeIsRecordedAgainFromThatStep(t *testing.T) {
	g := newRig(t)
	recipeFor(g, "host.example", 1000, 1000)
	g.r.Request(req("d1", "wifi", "https://host.example/f"))
	g.load("tab1", "ctx1", "https://host.example/f")
	for i := 0; i < maxClicks; i++ {
		g.msg("tab1", map[string]any{"type": "clicked", "index": 0})
		g.advance(effectWait)
	}
	if m := g.sent(); m["cmd"] != "record" {
		t.Fatalf("no switch to record: %v", m)
	}
	g.want(t, "notify DMS² needs a hand")
	g.msg("tab1", map[string]any{"type": "recorded", "step": map[string]any{"tag": "a", "id": "new"}})
	g.ev.Caught("tab1", Catch{URL: "https://cdn/f"})
	g.sync()
	g.said()
	rec := g.r.Recipes.Get("host.example")
	if len(rec.Steps) != 1 || rec.Steps[0].ID != "new" {
		t.Fatalf("recipe after recording from step 0: %+v", rec.Steps)
	}
}

func TestALearnJobSavesTheRecipeAndReportsNoLink(t *testing.T) {
	g := newRig(t)
	g.r.Request(Request{ID: "d1", Iface: "wifi", PageURL: "https://host.example/f", SocksPort: 4001, Learn: true})
	g.load("tab1", "ctx1", "https://host.example/f")
	g.msg("tab1", map[string]any{"type": "recorded", "step": map[string]any{"tag": "a"}})
	g.ev.Caught("tab1", Catch{URL: "https://cdn/f"})
	g.sync()
	g.want(t, "notify Show DMS² how to get the link\nhosts host.example\nstepBack d1 https://host.example/f\nlearnEnded d1 wifi")
	starts := g.starts
	g.r.Request(Request{ID: "d2", Iface: "wifi", PageURL: "https://host.example/g", SocksPort: 4001, Learn: true})
	g.want(t, "learnEnded d2 wifi")
	if g.starts != starts || g.count("Target.createTarget") != 1 {
		t.Fatal("a learn job for a host with a recipe opened something")
	}
}

func TestARealResolveDuringALearnJobMakesItReportItsLink(t *testing.T) {
	g := newRig(t)
	g.r.Request(Request{ID: "d1", Iface: "wifi", PageURL: "https://host.example/f", SocksPort: 4001, Learn: true})
	g.r.Request(req("d1", "wifi", "https://host.example/f"))
	g.load("tab1", "ctx1", "https://host.example/f")
	g.ev.Caught("tab1", Catch{URL: "https://cdn/f"})
	g.sync()
	if s := g.said(); !strings.Contains(s, "resolved d1 wifi https://cdn/f") || strings.Contains(s, "learnEnded") {
		t.Fatalf("hooks %s", s)
	}
}

func TestAnIdleRecordingAndAClosedWindowFail(t *testing.T) {
	g := newRig(t)
	g.r.Request(req("d1", "wifi", "https://host.example/f"))
	g.said()
	g.advance(recordIdle)
	g.want(t, "failed d1 wifi: nobody clicked through the page in time")

	g.r.Request(req("d2", "wifi", "https://other.example/f"))
	g.said()
	tab := fmt.Sprintf("tab%d", g.n)
	g.ev.Closed(tab)
	g.sync()
	g.want(t, "failed d2 wifi: the resolver window was closed")
}

func TestAtMostTwoWindowsPerHostTheRestWait(t *testing.T) {
	g := newRig(t)
	recipeFor(g, "host.example", 1000)
	for _, iface := range []string{"a", "b", "c"} {
		g.r.Request(req("d1", iface, "https://host.example/f"))
	}
	if n := g.count("Target.createTarget"); n != 2 {
		t.Fatalf("%d windows for one host", n)
	}
	g.ev.Caught("tab1", Catch{URL: "https://cdn/1"})
	g.sync()
	if n := g.count("Target.createTarget"); n != 3 {
		t.Fatal("the waiting job did not start when a window ended")
	}
}

// A cancelled download's jobs close their windows without a word, and a job
// waiting on the same host takes the place.
func TestCancelEndsADownloadsJobsQuietly(t *testing.T) {
	g := newRig(t)
	recipeFor(g, "host.example", 1000)
	g.r.Request(req("d1", "a", "https://host.example/f"))
	g.r.Request(req("d1", "b", "https://host.example/f"))
	g.r.Request(req("d2", "a", "https://host.example/g")) // waits: two windows for the host
	if n := g.count("Target.createTarget"); n != 2 {
		t.Fatalf("%d windows, want 2", n)
	}
	g.said()
	g.r.Cancel("d1")
	g.sync()
	if n := g.count("Target.closeTarget"); n != 2 {
		t.Fatalf("%d windows closed, want d1's two", n)
	}
	if n := g.count("Target.createTarget"); n != 3 {
		t.Fatal("the waiting job did not start")
	}
	g.advance(10 * time.Minute)
	if s := g.said(); strings.Contains(s, "d1") {
		t.Fatalf("a hook fired for the cancelled download: %s", s)
	}
}

func TestADeadBrowserFailsItsJobsAndTheNextResolveStartsANewOne(t *testing.T) {
	g := newRig(t)
	g.r.Request(req("d1", "wifi", "https://host.example/f"))
	g.said()
	g.r.mu.Lock()
	h := g.r.b.(*fakeHandle)
	g.r.mu.Unlock()
	h.Close()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if s := g.said(); s != "" {
			if s != "failed d1 wifi: the resolver browser closed" {
				t.Fatalf("hooks %s", s)
			}
			g.r.Request(req("d2", "wifi", "https://host.example/g"))
			if g.starts != 2 {
				t.Fatal("no new browser")
			}
			return
		}
	}
	t.Fatal("the job did not fail")
}

func TestTheRulesAgreeWithTheExtension(t *testing.T) {
	if budget(0) != 15*time.Second || budget(1000) != 17*time.Second {
		t.Error("budget")
	}
	r := &Recipe{Steps: []Step{{Wait: 1000}, {Wait: 0}}, DownloadWait: 2000}
	if got := replayTimeout(r); got != 17*time.Second+15*time.Second+19*time.Second+time.Minute {
		t.Errorf("replayTimeout %v", got)
	}
	old := &Recipe{Steps: []Step{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	if got := recorded(old, 1, []Step{{ID: "x"}}, 7, time.UnixMilli(5)); len(got.Steps) != 2 || got.Steps[0].ID != "a" || got.Steps[1].ID != "x" || got.DownloadWait != 7 || got.Learned != 5 {
		t.Errorf("recorded %+v", got)
	}
	if got := withMeasuredWaits(r, map[int]int64{1: 300}, 0, false); got.Steps[0].Wait != 1000 || got.Steps[1].Wait != 300 || got.DownloadWait != 2000 || r.Steps[1].Wait != 0 {
		t.Errorf("withMeasuredWaits %+v (old %+v)", got, r)
	}
	if afterClick(map[string]bool{"newTab": true}, 0, 1) != "done" || afterClick(nil, time.Second, 1) != "wait" ||
		afterClick(nil, effectWait, 4) != "retry" || afterClick(nil, effectWait, 5) != "giveUp" {
		t.Error("afterClick")
	}
	if popupVerdict("a.com", "a.com", 0, false) != "keep" || popupVerdict("a.com", "b.com", time.Second, false) != "wait" ||
		popupVerdict("a.com", "b.com", popupGrace, false) != "close" || popupVerdict("a.com", "b.com", 0, true) != "keep" {
		t.Error("popupVerdict")
	}
	if siteOf("https://dl.host.example/x") != "host.example" || siteOf("bad url\x00") != "" {
		t.Error("siteOf")
	}
}

func TestRecipesSurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	a, _ := OpenRecipes(path)
	a.Set("h", &Recipe{Steps: []Step{{ID: "x", Wait: 9}}})
	b, err := OpenRecipes(path)
	if err != nil || b.Get("h") == nil || b.Get("h").Steps[0].Wait != 9 {
		t.Fatalf("reopened %v %v", b.Get("h"), err)
	}
	b.Set("h", nil)
	c, _ := OpenRecipes(path)
	if len(c.Hosts()) != 0 {
		t.Fatal("a forgotten recipe came back")
	}
}

// sync waits until the events fired so far have been handled.
func (g *rig) sync() {
	g.r.mu.Lock()
	q := g.r.q
	g.r.mu.Unlock()
	if q == nil {
		return
	}
	done := make(chan struct{})
	q.post(func() { close(done) })
	<-done
}
