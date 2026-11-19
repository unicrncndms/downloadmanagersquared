package resolver

import (
	"encoding/json"
	"log"
	"slices"
	"sync"
	"time"
)

// The resolve loop, ported from extension/background/resolver.js (and
// resolver-messages.js): open the landing page in the interface's context,
// replay the host's recipe click by click with a budget per step, show the
// window for a captcha, judge the tabs a click opens, record when the recipe
// fails or there is none, and report the link the site hands out.

// Request is one resolve the engine asks for.
type Request struct {
	ID, Iface, IfaceName, PageURL string
	SocksPort                     int
	Learn                         bool     // only record the recipe (Record recipe): no link is reported
	Cookies                       []Cookie // the user's login for the landing page, from the download's browser
	Pages                         []string // the download's page history, for the record bar's way back
	Tokens                        any      // the record bar's accent (lib/accent.js accentTokens), nil for its own
	From                          string   // the download's browser, e.g. "Google Chrome": a recipe recorded here says so
}

// Hooks tell the engine how resolves end. They run after the resolver's lock
// is released, in order, so they may call the Resolver.
type Hooks struct {
	Resolved      func(id, iface, url, cookie, referer string)
	Failed        func(id, iface, why string)
	LearnEnded    func(id, iface string)
	SetPageURL    func(id, pageURL string) // the landing page the user really started from
	LearnStepBack func(id, startURL string)
	RecipeHosts   func(hosts []string)
	Notify        func(title, msg string)
}

// Browser is the running browser a Driver drives (a *cdp.Browser).
type Browser interface {
	Done() <-chan struct{}
	Close()
}

// Resolver runs resolves in a browser it starts on the first one and closes
// once none has run for Idle. Safe for concurrent use.
type Resolver struct {
	Recipes *Recipes
	Hooks   Hooks
	Start   func(Events) (*Driver, Browser, error) // e.g. Start(exe, dir, ev)
	Idle    time.Duration
	Screen  func() Bounds // where a shown window goes

	// For tests: the clock and timers.
	now   func() time.Time
	after func(time.Duration, func()) (stop func())

	mu       sync.Mutex
	d        *Driver
	b        Browser
	idleStop func()
	jobs     []*job // in arrival order: learningPlace counts by it
	byTab    map[string]*job
	popups   map[string]*popup
	ifaces   map[string]bool // interfaces with a context
	out      []func()        // hooks to run once the lock is released
	q        *queue          // the browser's events, see driver
}

type job struct {
	key, id, iface, ifaceName, pageURL, host string
	socksPort                                int
	cookies                                  []Cookie
	pages                                    []string
	tokens                                   any
	from                                     string

	learn, done, ended bool
	mode               string // new, waiting, replay, record
	step, recordFrom   int
	recorded           []Step
	tabs               []string
	tab                string // the tab the job works in
	loading, skipped   bool
	captcha, minimized bool
	noRecorder         bool // the page script couldn't start recording: what it saved is no recipe
	timers             map[string]*timer
	clicked            *clicked
	mark, lastClick    time.Time
	measured           map[int]int64
	budgetFor          int
	retried            map[int]bool
	activity           time.Time
	startURL, lastURL  string
}

type clicked struct {
	index, clicks int
	at, mark      time.Time
	effects       map[string]bool
}

type popup struct {
	job      *job
	url      string
	openedAt time.Time
	stop     func()
}

type timer struct{ stop func() }

func (r *Resolver) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Resolver) timerAfter(d time.Duration, f func()) func() {
	if r.after != nil {
		return r.after(d, f)
	}
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// lock and unlock around every entry point: unlock runs the queued hooks.
func (r *Resolver) lock() {
	r.mu.Lock()
	if r.byTab == nil {
		r.byTab, r.popups, r.ifaces = map[string]*job{}, map[string]*popup{}, map[string]bool{}
	}
}

func (r *Resolver) unlock() {
	out := r.out
	r.out = nil
	r.mu.Unlock()
	for _, f := range out {
		f()
	}
}

func (r *Resolver) later(f func()) { r.out = append(r.out, f) }

func (r *Resolver) notify(title, msg string) {
	if r.Hooks.Notify != nil {
		r.later(func() { r.Hooks.Notify(title, msg) })
	}
}

func (r *Resolver) setTimer(j *job, name string, d time.Duration, fn func()) {
	r.clearTimer(j, name)
	t := &timer{}
	j.timers[name] = t
	t.stop = r.timerAfter(d, func() {
		r.lock()
		defer r.unlock()
		if j.done || j.timers[name] != t {
			return
		}
		delete(j.timers, name)
		fn()
	})
}

func (r *Resolver) clearTimer(j *job, name string) {
	if t := j.timers[name]; t != nil {
		t.stop()
		delete(j.timers, name)
	}
}

func (r *Resolver) clearTimers(j *job) {
	for name := range j.timers {
		r.clearTimer(j, name)
	}
}

func (r *Resolver) find(key string) *job {
	for _, j := range r.jobs {
		if j.key == key {
			return j
		}
	}
	return nil
}

// Request starts a resolve, or makes a running learn job report its link.
func (r *Resolver) Request(req Request) {
	r.lock()
	defer r.unlock()
	key := req.ID + "|" + req.Iface
	if running := r.find(key); running != nil {
		// The link expired while its recipe was being recorded: that link is wanted after all.
		if !req.Learn && running.learn {
			running.learn = false
		}
		return
	}
	name := req.IfaceName
	if name == "" {
		name = req.Iface
	}
	j := &job{
		key: key, id: req.ID, iface: req.Iface, ifaceName: name, pageURL: req.PageURL, host: hostOf(req.PageURL),
		socksPort: req.SocksPort, cookies: req.Cookies, pages: req.Pages, tokens: req.Tokens, from: req.From,
		learn: req.Learn, mode: "new", timers: map[string]*timer{}, measured: map[int]int64{}, budgetFor: -1, retried: map[int]bool{},
	}
	r.jobs = append(r.jobs, j)
	if r.idleStop != nil {
		r.idleStop()
		r.idleStop = nil
	}
	r.startOrQueue(j)
}

// startChoice: "endLearn", "replay", "waiting" or "record" (route.js).
func (r *Resolver) startChoice(j *job, hasRecipe bool) string {
	if hasRecipe && j.learn {
		return "endLearn"
	}
	open, recording := 0, false
	for _, o := range r.jobs {
		if o == j || o.host != j.host {
			continue
		}
		if !o.done && (o.mode == "replay" || o.mode == "record") {
			open++
		}
		if o.mode == "record" {
			recording = true
		}
	}
	if open >= maxWindows {
		return "waiting"
	}
	if hasRecipe {
		return "replay"
	}
	if recording {
		return "waiting"
	}
	return "record"
}

func (r *Resolver) startOrQueue(j *job) {
	switch choice := r.startChoice(j, r.Recipes.Get(j.host) != nil); choice {
	case "waiting":
		j.mode = "waiting"
	case "endLearn":
		r.endLearn(j)
	default:
		j.mode = choice // now, so jobs starting after this count it
		r.launch(j, choice)
	}
}

func (r *Resolver) driver() (*Driver, error) {
	if r.d != nil {
		return r.d, nil
	}
	// The browser's events go through a queue of the resolver's own: its
	// event goroutine must never wait for the resolver's lock, which Open
	// holds while it waits for that goroutine to attach the page.
	q := &queue{}
	r.q = q
	go q.run(func(f func()) { r.lock(); defer r.unlock(); f() })
	d, b, err := r.Start(Events{
		Opened:  func(t, o, u string) { q.post(func() { r.onOpened(t, o, u) }) },
		Loading: func(t, u string) { q.post(func() { r.onLoading(t, u) }) },
		Loaded:  func(t, u string) { q.post(func() { r.onLoaded(t, u) }) },
		Closed:  func(t string) { q.post(func() { r.onClosed(t) }) },
		Caught:  func(t string, c Catch) { q.post(func() { r.onCaught(t, c) }) },
		Message: func(t, p string) { q.post(func() { r.onMessage(t, p) }) },
	})
	if err != nil {
		q.close()
		return nil, err
	}
	r.d, r.b = d, b
	r.ifaces = map[string]bool{}
	go func() {
		<-b.Done()
		q.close()
		r.lock()
		defer r.unlock()
		if r.b != b {
			return
		}
		r.d, r.b = nil, nil
		for _, j := range slices.Clone(r.jobs) {
			if j.mode != "waiting" {
				r.fail(j, "the resolver browser closed")
			}
		}
	}()
	return d, nil
}

// queue runs posted funcs one at a time, in order, without ever blocking post.
type queue struct {
	mu     sync.Mutex
	fs     []func()
	wake   chan struct{}
	closed bool
}

func (q *queue) post(f func()) {
	q.mu.Lock()
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
	if !q.closed {
		q.fs = append(q.fs, f)
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.post(func() {})
}

func (q *queue) run(do func(func())) {
	q.mu.Lock()
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
	wake := q.wake
	q.mu.Unlock()
	for range wake {
		for {
			q.mu.Lock()
			if len(q.fs) == 0 {
				done := q.closed
				q.mu.Unlock()
				if done {
					return
				}
				break
			}
			f := q.fs[0]
			q.fs = q.fs[1:]
			q.mu.Unlock()
			do(f)
		}
	}
}

func (r *Resolver) screen() Bounds {
	if r.Screen != nil {
		return r.Screen()
	}
	return Bounds{Left: 100, Top: 60, Width: 1280, Height: 900}
}

func (r *Resolver) launch(j *job, mode string) {
	if j.socksPort <= 0 {
		r.fail(j, "the connection has no proxy to resolve through")
		return
	}
	d, err := r.driver()
	if err != nil {
		r.fail(j, "could not start the resolver browser: "+err.Error())
		return
	}
	ctx, err := d.Context(j.iface, j.socksPort)
	if err != nil {
		r.fail(j, "could not make a browser context: "+err.Error())
		return
	}
	r.ifaces[j.iface] = true
	j.mode, j.step, j.recordFrom, j.recorded = mode, 0, 0, nil
	j.measured, j.budgetFor = map[int]int64{}, -1
	visible := mode == "record"
	// The page opens with the user's login for its site, not as a stranger.
	d.SetCookies(ctx, j.cookies)
	tab, err := d.Open(ctx, j.pageURL, visible)
	if err != nil {
		r.fail(j, "could not open the page: "+err.Error())
		return
	}
	j.tab, j.minimized = tab, !visible
	r.addTab(j, tab)
	log.Printf("resolver: %s via %s: %s in %s", j.host, j.ifaceName, mode, tab)
	if visible {
		d.Show(tab, r.screen(), true)
		r.notify("Show DMS² how to get the link", "Click through "+j.host+" for "+j.ifaceName+" as you normally would. Your clicks are remembered for next time.")
		r.touch(j, "")
	} else {
		r.setTimer(j, "overall", replayTimeout(r.Recipes.Get(j.host)), func() { r.fail(j, "timed out") })
	}
}

func (r *Resolver) addTab(j *job, tab string) {
	if !slices.Contains(j.tabs, tab) {
		j.tabs = append(j.tabs, tab)
	}
	r.byTab[tab] = j
}

// touch: something happened while a person is needed; they get another five minutes.
func (r *Resolver) touch(j *job, why string) {
	if why == "" {
		why = "nobody clicked through the page in time"
	}
	j.activity = r.clock()
	r.setTimer(j, "overall", recordIdle, func() { r.fail(j, why) })
}

// show brings a minimized window out, once. A window already shown is the
// person's: it isn't moved, sized or raised again (a captcha breaks under that).
func (r *Resolver) show(j *job, focus bool) {
	if r.d != nil && j.minimized {
		r.d.Show(j.tab, r.screen(), focus)
		j.minimized = false
	}
}

// secondChance: a step out of time in a minimized window gets one more go
// with the window shown but not focused: some pages only count down while visible.
func (r *Resolver) secondChance(j *job, index int) bool {
	if j.retried[index] || !j.minimized {
		return false
	}
	j.retried[index] = true
	r.show(j, false)
	r.setTimer(j, "overall", replayTimeout(r.Recipes.Get(j.host)), func() { r.fail(j, "timed out") })
	return true
}

func (r *Resolver) budgetOut(j *job, index int, d time.Duration, why string) {
	if j.done || j.mode != "replay" {
		return
	}
	if r.secondChance(j, index) {
		r.setTimer(j, "budget", d, func() { r.budgetOut(j, index, d, why) })
		if index < len(r.steps(j)) {
			r.sendStep(j)
		}
		return
	}
	r.switchToRecord(j, index, why)
}

func (r *Resolver) steps(j *job) []Step {
	if rec := r.Recipes.Get(j.host); rec != nil {
		return rec.Steps
	}
	return nil
}

func (r *Resolver) onOpened(tab, opener, url string) {
	j := r.byTab[opener]
	if j == nil || j.done {
		return
	}
	r.addTab(j, tab)
	if j.mode == "replay" {
		p := &popup{job: j, url: url, openedAt: r.clock()}
		r.popups[tab] = p
		r.judgePopup(tab, p)
	}
}

// judgePopup: a tab a replayed click opened carries on the same site;
// another site gets a moment to start a download, else it is an ad and closes.
func (r *Resolver) judgePopup(tab string, p *popup) {
	if p.stop != nil {
		p.stop()
		p.stop = nil
	}
	j := p.job
	if j.done {
		delete(r.popups, tab)
		return
	}
	site := ""
	if p.url != "" && p.url != "about:blank" {
		site = siteOf(p.url)
	}
	opener := j.lastURL
	if opener == "" {
		opener = j.pageURL
	}
	age := r.clock().Sub(p.openedAt)
	switch popupVerdict(siteOf(opener), site, age, false) {
	case "wait":
		p.stop = r.timerAfter(popupGrace-age+time.Millisecond, func() {
			r.lock()
			defer r.unlock()
			if r.popups[tab] == p {
				r.judgePopup(tab, p)
			}
		})
	case "keep":
		delete(r.popups, tab)
		j.tab = tab // the next page opened in a tab of its own: follow it there
		r.clickEffect(j, "newTab")
	default:
		log.Printf("resolver: %s via %s: closed a tab a click opened on %s, taken for an ad", j.host, j.ifaceName, site)
		delete(r.popups, tab)
		if r.d != nil {
			r.d.Close(tab)
		}
	}
}

func (r *Resolver) onLoading(tab, url string) {
	if p := r.popups[tab]; p != nil {
		p.url = url
		r.judgePopup(tab, p)
	}
	j := r.byTab[tab]
	if j == nil || j.done || tab != j.tab {
		return
	}
	j.loading = true
	r.clickEffect(j, "navigation")
}

func (r *Resolver) onLoaded(tab, url string) {
	j := r.byTab[tab]
	if j == nil || j.done || tab != j.tab || !isHTTP(url) {
		return // about:blank, which a window opens on, is no page to drive
	}
	j.loading = false
	j.lastURL = url
	j.mark = r.clock()
	if j.mode == "record" {
		r.touch(j, "")
	}
	// A new page may mean the captcha is behind us, or be the challenge
	// reloading itself: done only if the new page reports none in time (its
	// script waits CAPTCHA_GRACE before it does). Meanwhile the window stays
	// as it is: moving or minimizing it under the person solving breaks it.
	if j.captcha {
		r.setTimer(j, "captchaGone", captchaSettle, func() {
			if j.captcha {
				r.captchaDone(j)
			}
		})
	}
	r.drive(j)
}

func (r *Resolver) drive(j *job) {
	if r.d == nil {
		return
	}
	if err := r.d.Drive(j.tab); err != nil {
		log.Printf("resolver: no page script in %s's page: %v", j.host, err) // e.g. an error page; the overall timer catches a stuck job
		return
	}
	log.Printf("resolver: %s via %s: page script in %s (%s)", j.host, j.ifaceName, j.tab, j.mode)
	switch j.mode {
	case "record":
		r.sendRecord(j)
	case "replay":
		r.sendStep(j)
	}
}

// learningPlace: which of the site's connections still to learn this job is.
func (r *Resolver) learningPlace(j *job) (n, of int) {
	var mine []*job
	for _, o := range r.jobs {
		if !o.done && o.host == j.host && (o.mode == "record" || o.mode == "waiting") {
			mine = append(mine, o)
		}
	}
	return max(1, slices.Index(mine, j)+1), max(1, len(mine))
}

// sendRecord: recording, with the download's pages on this site for the record
// bar's Back and Forward: back in case DMS² opened a page that only worked for
// the first connection, forward in case it started too early (a recipe that
// starts on a list of files only fits one of them). The bar finds itself in
// the list by its own address.
func (r *Resolver) sendRecord(j *job) {
	pages := []string{}
	for _, p := range j.pages {
		if siteOf(p) == siteOf(j.pageURL) { // the page before them is often a search engine
			pages = append(pages, p)
		}
	}
	log.Printf("resolver: %s: record bar, page %d of the %d on this site the download came through", j.host, slices.Index(pages, j.pageURL)+1, len(pages))
	n, of := r.learningPlace(j)
	tokens := j.tokens
	if tokens == nil {
		tokens = defaultTokens // the record bar needs colours (resolver-content.js BAR_CSS)
	}
	if err := r.send(j, map[string]any{"cmd": "record", "ifaceName": j.ifaceName, "pages": pages, "n": n, "of": of, "learn": j.learn, "tokens": tokens}); err != nil {
		log.Printf("resolver: recording on %s didn't start: %v", j.host, err)
		j.noRecorder = true
	} else {
		j.noRecorder = false
	}
}

// defaultTokens are lib/accent.js accentTokens("#1d4ed8"), the record bar's
// colours when the download's browser didn't say its accent.
var defaultTokens = map[string]map[string]string{
	"light": {"accent": "#1d4ed8", "accentSoft": "#e8edfb", "onAccent": "#ffffff"},
	"dark":  {"accent": "#809ce9", "accentSoft": "#2d3445", "onAccent": "#0b1020"},
}

func (r *Resolver) send(j *job, msg any) error {
	if r.d == nil {
		return nil
	}
	return r.d.Send(j.tab, msg)
}

// captchaShown: a captcha needs a person; show the window and hold the timeouts.
func (r *Resolver) captchaShown(j *job) {
	if j.captcha {
		r.clearTimer(j, "captchaGone") // the page after a reload has it still
		return
	}
	j.captcha = true
	log.Printf("resolver: %s via %s: captcha, asking for help", j.host, j.ifaceName)
	r.markCaptcha(j)
	r.clearTimer(j, "budget")
	r.clearTimer(j, "effect")
	j.clicked = nil
	r.touch(j, "the captcha wasn't solved in time")
	r.show(j, true)
	r.notify("DMS² needs you to solve a captcha", j.host+" asks for a captcha on "+j.ifaceName+". The window is open; DMS² continues once it's solved.")
}

// markCaptcha tells the driver which of j's pages show a captcha.
func (r *Resolver) markCaptcha(j *job) {
	if r.d == nil {
		return
	}
	for _, t := range j.tabs {
		r.d.SetCaptcha(t, j.captcha)
	}
}

func (r *Resolver) captchaDone(j *job) {
	r.clearTimer(j, "captchaGone")
	j.captcha = false
	log.Printf("resolver: %s via %s: captcha solved", j.host, j.ifaceName)
	r.markCaptcha(j)
	if j.mode != "replay" {
		r.touch(j, "")
		return
	}
	// Back out of the way; the page's script carries on with the next click itself.
	if r.d != nil {
		r.d.Minimize(j.tab)
		j.minimized = true
	}
	r.setTimer(j, "overall", replayTimeout(r.Recipes.Get(j.host)), func() { r.fail(j, "timed out") })
	j.budgetFor = -1 // the step gets its budget afresh
	r.sendStep(j)
}

// sendStep sends the step to click, with its budget.
func (r *Resolver) sendStep(j *job) {
	if j.captcha {
		return // continued once the captcha is solved
	}
	steps := r.steps(j)
	if j.step >= len(steps) {
		// All clicks done: the site's timer runs, then the download should start.
		var wait int64
		if rec := r.Recipes.Get(j.host); rec != nil {
			wait = rec.DownloadWait
		}
		r.armBudget(j, len(steps), budget(wait), "The download didn't start")
		return
	}
	log.Printf("resolver: %s via %s: replay step %d of %d (%s %q) in %s", j.host, j.ifaceName, j.step+1, len(steps), steps[j.step].Tag, steps[j.step].Text, j.tab)
	b := budget(steps[j.step].Wait)
	r.armBudget(j, j.step, b+time.Second, "The page didn't let DMS² click in time") // the page reports first
	var next any
	if j.step+1 < len(steps) {
		next = steps[j.step+1]
	}
	r.send(j, map[string]any{"cmd": "replay", "step": steps[j.step], "index": j.step, "next": next, "budget": b.Milliseconds()})
}

func (r *Resolver) armBudget(j *job, index int, d time.Duration, why string) {
	if j.budgetFor == index {
		return
	}
	j.budgetFor = index
	r.setTimer(j, "budget", d, func() { r.budgetOut(j, index, d, why) })
}

// clickEffect: a replayed click showed an effect, the step is done. After a
// navigation the next step is sent once the new page has loaded.
func (r *Resolver) clickEffect(j *job, kind string) {
	c := j.clicked
	if c == nil || j.mode != "replay" {
		return
	}
	c.effects[kind] = true
	if afterClick(c.effects, r.clock().Sub(c.at), c.clicks) != "done" {
		return
	}
	j.clicked = nil
	r.clearTimer(j, "effect")
	j.measured[c.index] = c.at.Sub(c.mark).Milliseconds()
	j.mark = c.at
	j.step = c.index + 1
	if kind != "navigation" && !j.loading {
		r.sendStep(j)
	}
}

// judgeClick: no effect within the wait; click again, or after too many ask the user.
func (r *Resolver) judgeClick(j *job) {
	c := j.clicked
	if c == nil || j.done || j.mode != "replay" {
		return
	}
	since := r.clock().Sub(c.at)
	switch afterClick(c.effects, since, c.clicks) {
	case "wait":
		r.setTimer(j, "effect", max(time.Millisecond, effectWait-since), func() { r.judgeClick(j) })
	case "retry":
		r.sendStep(j)
	case "giveUp":
		j.clicked = nil
		r.switchToRecord(j, c.index, "Clicking the button had no effect")
	}
}

func (r *Resolver) switchToRecord(j *job, from int, why string) {
	if j.done {
		return
	}
	log.Printf("resolver: %s via %s: replay gave up at step %d of %d (%s), recording from there", j.host, j.ifaceName, from+1, len(r.steps(j)), why)
	j.mode, j.recordFrom, j.recorded = "record", from, nil
	r.clearTimers(j)
	r.show(j, true)
	r.notify("DMS² needs a hand", why+" on "+j.host+" ("+j.ifaceName+"). Please click through; your clicks will be remembered.")
	r.sendRecord(j)
	r.touch(j, "")
}

// onCaught: the download a job's page started is the link.
func (r *Resolver) onCaught(tab string, c Catch) {
	j := r.byTab[tab]
	if j == nil {
		return
	}
	cookie := c.Cookie
	if cookie == "" && r.d != nil {
		cookie = r.d.CookieHeader(tab, c.URL)
	}
	r.finish(j, c.URL, cookie)
}

func (r *Resolver) finish(j *job, url, cookie string) {
	if j.done {
		return
	}
	j.done = true
	r.clearTimers(j)
	if j.startURL != "" && j.startURL != j.pageURL && isHTTP(j.startURL) {
		r.correctPage(j)
	}
	if j.mode == "record" {
		r.saveRecipe(j)
		// Where the first click was is where to start on this site from now on.
		if j.recordFrom == 0 && j.startURL != "" && r.Hooks.LearnStepBack != nil {
			id, start := j.id, j.startURL
			r.later(func() { r.Hooks.LearnStepBack(id, start) })
		}
	}
	if j.mode == "replay" {
		if rec := r.Recipes.Get(j.host); rec != nil {
			// The site's waits this time are the ones to expect next time.
			r.Recipes.Set(j.host, withMeasuredWaits(rec, j.measured, r.clock().Sub(j.lastClick).Milliseconds(), !j.lastClick.IsZero()))
		}
	}
	// A learn job's link is thrown away: the download keeps its own.
	if j.learn {
		r.endLearn(j)
		return
	}
	referer := j.lastURL
	if referer == "" {
		referer = j.pageURL
	}
	if r.Hooks.Resolved != nil {
		id, iface := j.id, j.iface
		r.later(func() { r.Hooks.Resolved(id, iface, url, cookie, referer) })
	}
	r.cleanup(j)
}

func (r *Resolver) fail(j *job, why string) {
	if j.learn {
		r.endLearn(j) // nothing changes for the download
		return
	}
	if j.done {
		return
	}
	j.done = true
	r.clearTimers(j)
	if r.Hooks.Failed != nil {
		id, iface := j.id, j.iface
		r.later(func() { r.Hooks.Failed(id, iface, why) })
	}
	r.cleanup(j)
}

// endLearn: a learn job is over, recipe saved or not.
func (r *Resolver) endLearn(j *job) {
	first := !j.ended
	j.done, j.ended = true, true
	r.clearTimers(j)
	if first && r.Hooks.LearnEnded != nil {
		id, iface := j.id, j.iface
		r.later(func() { r.Hooks.LearnEnded(id, iface) })
	}
	if slices.Contains(r.jobs, j) {
		r.cleanup(j)
	}
}

// correctPage remembers the landing page the user really started from, for
// this download and its connections still waiting.
func (r *Resolver) correctPage(j *job) {
	page := j.startURL
	j.pageURL, j.host = page, hostOf(page)
	if r.Hooks.SetPageURL != nil {
		id := j.id
		r.later(func() { r.Hooks.SetPageURL(id, page) })
	}
	for _, o := range r.jobs {
		if o != j && o.id == j.id && o.mode == "waiting" {
			o.pageURL, o.host = page, j.host
		}
	}
}

func (r *Resolver) saveRecipe(j *job) {
	if j.noRecorder && len(j.recorded) == 0 {
		return // nothing was watched: the next connection records again
	}
	var wait int64
	if !j.lastClick.IsZero() {
		wait = r.clock().Sub(j.lastClick).Milliseconds()
	}
	rec := recorded(r.Recipes.Get(j.host), j.recordFrom, j.recorded, wait, r.clock())
	rec.From = j.from
	r.Recipes.Set(j.host, rec)
	r.sendRecipeHosts()
}

func (r *Resolver) sendRecipeHosts() {
	if r.Hooks.RecipeHosts != nil {
		hosts := r.Recipes.Hosts()
		r.later(func() { r.Hooks.RecipeHosts(hosts) })
	}
}

// ForgetRecipe drops host's recipe.
func (r *Resolver) ForgetRecipe(host string) error {
	r.lock()
	defer r.unlock()
	err := r.Recipes.Set(host, nil)
	r.sendRecipeHosts()
	return err
}

func (r *Resolver) cleanup(j *job) {
	for _, t := range j.tabs {
		delete(r.byTab, t)
		delete(r.popups, t)
	}
	r.jobs = slices.DeleteFunc(r.jobs, func(o *job) bool { return o == j })
	if r.d != nil {
		for _, t := range j.tabs {
			r.d.Close(t)
		}
	}
	// An interface's context goes with its last running resolve, its pages with it.
	for iface := range r.ifaces {
		if !slices.ContainsFunc(r.jobs, func(o *job) bool { return o.iface == iface && o.mode != "waiting" }) {
			delete(r.ifaces, iface)
			if r.d != nil {
				r.d.DropContext(iface)
			}
		}
	}
	for _, o := range slices.Clone(r.jobs) {
		if o.mode == "waiting" && o.host == j.host && slices.Contains(r.jobs, o) {
			r.startOrQueue(o)
		}
	}
	if len(r.jobs) == 0 && r.b != nil && r.idleStop == nil {
		b := r.b
		r.idleStop = r.timerAfter(r.Idle, func() {
			r.lock()
			defer r.unlock()
			if len(r.jobs) > 0 || r.b != b {
				return
			}
			r.d, r.b, r.idleStop = nil, nil, nil
			r.later(b.Close)
		})
	}
}

func (r *Resolver) onClosed(tab string) {
	delete(r.popups, tab)
	j := r.byTab[tab]
	if j == nil {
		return
	}
	delete(r.byTab, tab)
	j.tabs = slices.DeleteFunc(j.tabs, func(t string) bool { return t == tab })
	if j.done {
		return
	}
	if len(j.tabs) == 0 {
		r.fail(j, "the resolver window was closed")
		return
	}
	if j.tab == tab {
		j.tab = j.tabs[len(j.tabs)-1]
	}
}

// onMessage: what the page script reports (resolver-messages.js).
func (r *Resolver) onMessage(tab, payload string) {
	var m struct {
		Resolver bool   `json:"resolver"`
		Type     string `json:"type"`
		Index    int    `json:"index"`
		Kind     string `json:"kind"`
		Step     Step   `json:"step"`
	}
	if json.Unmarshal([]byte(payload), &m) != nil || !m.Resolver {
		return
	}
	j := r.byTab[tab]
	if j == nil || j.done {
		return
	}
	now := r.clock()
	switch m.Type {
	case "clicked":
		if j.mode != "replay" {
			return
		}
		c := &clicked{index: m.Index, at: now, clicks: 1, mark: j.mark, effects: map[string]bool{}}
		if p := j.clicked; p != nil && p.index == m.Index {
			c.clicks, c.mark = p.clicks+1, p.mark
		}
		j.clicked, j.lastClick = c, now
		r.setTimer(j, "effect", effectWait, func() { r.judgeClick(j) })
	case "effect":
		if j.clicked != nil && j.clicked.index == m.Index {
			r.clickEffect(j, m.Kind)
		}
	case "notFound":
		if j.mode != "replay" {
			return
		}
		steps := r.steps(j)
		// A remembered click may have been optional (a banner): try skipping it once.
		if !j.skipped && m.Index+1 < len(steps) {
			j.skipped = true
			j.step = m.Index + 1
			r.sendStep(j)
		} else if m.Index < len(steps) {
			r.budgetOut(j, m.Index, budget(steps[m.Index].Wait), "Couldn't find the button to click")
		}
	case "captcha":
		r.captchaShown(j)
	case "captchaDone":
		if j.captcha {
			r.captchaDone(j)
		}
	case "skip": // the same as closing the window
		r.fail(j, "the resolver window was closed")
	case "recorded":
		if j.mode != "record" {
			return
		}
		// The page of your first click is the real landing page.
		if j.recordFrom == 0 && len(j.recorded) == 0 {
			j.startURL = j.lastURL
			if r.d != nil {
				if u := r.d.URLOf(tab); u != "" {
					j.startURL = u
				}
			}
		}
		st := m.Step
		st.Wait = now.Sub(j.mark).Milliseconds()
		j.recorded = append(j.recorded, st)
		j.mark, j.lastClick = now, now
		r.touch(j, "")
	}
}

// Cancel ends every job of download id (its links and a recording alike) and
// closes its pages, with no hook: the download is gone, or needs no link now.
func (r *Resolver) Cancel(id string) {
	r.lock()
	defer r.unlock()
	for _, j := range slices.Clone(r.jobs) {
		if j.id != id || !slices.Contains(r.jobs, j) {
			continue
		}
		j.done, j.ended = true, true
		r.clearTimers(j)
		for _, p := range r.popups {
			if p.job == j && p.stop != nil {
				p.stop()
			}
		}
		r.cleanup(j)
	}
}

// Close ends every resolve and the browser (the engine is quitting).
func (r *Resolver) Close() {
	r.lock()
	for _, j := range slices.Clone(r.jobs) {
		r.fail(j, "the engine is quitting")
	}
	if r.idleStop != nil {
		r.idleStop()
		r.idleStop = nil
	}
	b := r.b
	r.d, r.b = nil, nil
	r.unlock()
	if b != nil {
		b.Close()
	}
}
