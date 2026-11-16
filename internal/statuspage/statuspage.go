// Package statuspage probes the public apps and serves the public status
// page. It shows per-service up/degraded/down, 90 days of daily uptime, a
// cluster summary made of counts only, maintenance notices and past
// incidents. Pod names, node names, alert names and error text never appear
// on it: those stay in Discord.
package statuspage

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"
)

// State of one check.
type State string

const (
	Up          State = "up"
	Degraded    State = "degraded"
	Down        State = "down"
	Maintenance State = "maintenance"
	Unknown     State = "unknown"
)

func rank(s State) int {
	switch s {
	case Up:
		return 0
	case Maintenance:
		return 1
	case Degraded:
		return 2
	case Down:
		return 3
	}
	return -1
}

// metricValue is what plumb_status_state exports: 2 up, 1 degraded, 0 down.
func metricValue(s State) (float64, bool) {
	switch s {
	case Up:
		return 2, true
	case Degraded:
		return 1, true
	case Down:
		return 0, true
	}
	return 0, false
}

// Target is one public app, probed over HTTP.
type Target struct {
	Name  string `json:"name"`
	Group string `json:"group"`
	URL   string `json:"-"`
	Order int    `json:"-"`
}

// Component is a non-HTTP check shown under "Platform" (the cluster, the bot).
type Component struct {
	Name  string
	Check func(ctx context.Context) State
}

// Stat is one public number in the cluster summary. Counts and percentages
// only, never names.
type Stat struct {
	Label string `json:"label"`
	Value string `json:"value"`
	State State  `json:"state"`
}

type result struct {
	state State
	lat   time.Duration
	at    time.Time
}

// Options configure a Checker.
type Options struct {
	Title       string
	Host        string        // shown in the title bar, e.g. status.reyki.me
	Slow        time.Duration // slower than this is "degraded"
	Location    *time.Location
	NoticesFile string                                      // optional
	NoticeFeed  func(ctx context.Context) ([]Notice, error) // optional: /notice posts in Discord
	History     RangeQuerier                                // optional: VictoriaMetrics
	Discover    func(ctx context.Context) ([]Target, error) // optional: IngressRoute discovery
	Log         *slog.Logger
}

// Checker probes everything on an interval and builds the page.
type Checker struct {
	opt    Options
	static []Target
	http   *http.Client
	now    func() time.Time

	components []Component
	stats      func(ctx context.Context) []Stat

	mu          sync.RWMutex
	discovered  []Target
	latest      map[string]result
	latestStats []Stat
	history     History
	notices     []Notice
}

// NewChecker builds a checker for the given static targets.
func NewChecker(static []Target, opt Options) *Checker {
	if opt.Location == nil {
		opt.Location = time.Local
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	return &Checker{
		opt: opt, static: static, now: time.Now,
		http:   &http.Client{Timeout: 10 * time.Second},
		latest: map[string]result{},
	}
}

// AddComponent adds a check under "Platform".
func (c *Checker) AddComponent(comp Component) { c.components = append(c.components, comp) }

// SetStats sets the source of the cluster summary numbers.
func (c *Checker) SetStats(f func(ctx context.Context) []Stat) { c.stats = f }

// Run probes every interval and refreshes history, notices and discovery
// every 5 minutes, until ctx ends.
func (c *Checker) Run(ctx context.Context, interval time.Duration) {
	// Give the Discord connection a moment, so a fresh start doesn't record
	// the bot as down.
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
	}
	c.Refresh(ctx)
	c.CheckAll(ctx)
	probe := time.NewTicker(interval)
	refresh := time.NewTicker(5 * time.Minute)
	defer probe.Stop()
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-probe.C:
			c.CheckAll(ctx)
		case <-refresh.C:
			c.Refresh(ctx)
		}
	}
}

// Targets returns static targets plus discovered ones (static wins by name),
// sorted by group, order, name.
func (c *Checker) Targets() []Target {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := map[string]bool{}
	var out []Target
	for _, t := range append(slices.Clone(c.static), c.discovered...) {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		if t.Group == "" {
			t.Group = "Apps"
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Refresh reloads discovery, notices and history. Each part fails on its
// own: the page keeps the last good value.
func (c *Checker) Refresh(ctx context.Context) {
	now := c.now()
	if c.opt.Discover != nil {
		if ts, err := c.opt.Discover(ctx); err != nil {
			c.opt.Log.Warn("service discovery failed", "err", err)
		} else {
			c.mu.Lock()
			c.discovered = ts
			c.mu.Unlock()
		}
	}
	c.RefreshNotices(ctx)
	if c.opt.History != nil {
		if h, err := LoadHistory(ctx, c.opt.History, now, c.opt.Location); err != nil {
			c.opt.Log.Warn("loading history failed", "err", err)
		} else {
			c.mu.Lock()
			c.history = h
			c.mu.Unlock()
		}
	}
}

// RefreshNotices reloads the notices file and the Discord feed. Called by
// Refresh, and right after a /notice so the page updates at once.
func (c *Checker) RefreshNotices(ctx context.Context) {
	if c.opt.NoticesFile == "" && c.opt.NoticeFeed == nil {
		return
	}
	var all []Notice
	ok := true
	if c.opt.NoticesFile != "" {
		ns, err := LoadNotices(c.opt.NoticesFile)
		if err != nil {
			c.opt.Log.Warn("reading notices failed", "file", c.opt.NoticesFile, "err", err)
			ok = false
		}
		all = append(all, ns...)
	}
	if c.opt.NoticeFeed != nil {
		ns, err := c.opt.NoticeFeed(ctx)
		if err != nil {
			c.opt.Log.Warn("reading Discord notices failed", "err", err)
			ok = false
		}
		all = append(all, ns...)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok || len(all) > 0 {
		c.notices = all
	}
}

// CheckAll probes every target and component once, in parallel.
func (c *Checker) CheckAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range c.Targets() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, lat := c.probe(ctx, t.URL)
			if state == Down { // one retry, so a single dropped packet isn't an outage
				state, lat = c.probe(ctx, t.URL)
			}
			c.record(t.Name, state, lat)
		}()
	}
	for _, comp := range c.components {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.record(comp.Name, comp.Check(ctx), 0)
		}()
	}
	if c.stats != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := c.stats(ctx)
			c.mu.Lock()
			c.latestStats = st
			c.mu.Unlock()
		}()
	}
	wg.Wait()
}

func (c *Checker) probe(ctx context.Context, url string) (State, time.Duration) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Down, 0
	}
	req.Header.Set("User-Agent", "plumb-status/1")
	start := time.Now()
	resp, err := c.http.Do(req)
	lat := time.Since(start)
	if err != nil {
		c.opt.Log.Debug("probe failed", "url", url, "err", err)
		return Down, lat
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == 404 {
		return Down, lat
	}
	if lat > c.opt.Slow {
		return Degraded, lat
	}
	return Up, lat
}

func (c *Checker) record(name string, st State, lat time.Duration) {
	c.mu.Lock()
	c.latest[name] = result{st, lat, c.now()}
	c.mu.Unlock()
}
