// Package statuspage probes the public apps and serves a small public
// status page. It shows only per-service up/slow/down, never cluster
// internals (pods, nodes, errors stay in Discord).
package statuspage

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bitsandbricks-ch/plumb/internal/config"
)

// State of one check.
type State string

const (
	Up      State = "up"
	Slow    State = "slow"
	Down    State = "down"
	Unknown State = "unknown"
)

func rank(s State) int {
	switch s {
	case Up:
		return 0
	case Slow:
		return 1
	case Down:
		return 2
	}
	return -1
}

const (
	window  = 24 * time.Hour
	buckets = 48 // 30-minute bars
)

type sample struct {
	at    time.Time
	state State
}

// Service is the public view of one service.
type Service struct {
	Name      string    `json:"name"`
	State     State     `json:"state"`
	LatencyMS int64     `json:"latency_ms"`
	CheckedAt time.Time `json:"checked_at"`
	Uptime24h float64   `json:"uptime_24h"` // 0..100, -1 when no data
	Bars      []State   `json:"bars"`       // oldest first, 30 min each
}

// Page is everything the template and JSON endpoint show.
type Page struct {
	Title     string    `json:"title"`
	Overall   State     `json:"overall"`
	Services  []Service `json:"services"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Checker probes services on an interval and keeps 24h of history in memory.
type Checker struct {
	title    string
	services []config.Service
	slow     time.Duration
	http     *http.Client
	log      *slog.Logger
	now      func() time.Time

	mu      sync.RWMutex
	history map[string][]sample
	latest  map[string]Service
}

// NewChecker builds a checker.
func NewChecker(title string, services []config.Service, slow time.Duration, log *slog.Logger) *Checker {
	return &Checker{
		title: title, services: services, slow: slow, log: log, now: time.Now,
		http:    &http.Client{Timeout: 10 * time.Second},
		history: map[string][]sample{}, latest: map[string]Service{},
	}
}

// Run probes every interval until ctx ends.
func (c *Checker) Run(ctx context.Context, interval time.Duration) {
	c.CheckAll(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.CheckAll(ctx)
		}
	}
}

// CheckAll probes every service once, in parallel.
func (c *Checker) CheckAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range c.services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, lat := c.probe(ctx, s.URL)
			if state == Down { // one retry, so a single dropped packet isn't an outage
				state, lat = c.probe(ctx, s.URL)
			}
			c.record(s.Name, state, lat)
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
		c.log.Debug("probe failed", "url", url, "err", err)
		return Down, lat
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Down, lat
	}
	if lat > c.slow {
		return Slow, lat
	}
	return Up, lat
}

func (c *Checker) record(name string, st State, lat time.Duration) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	h := append(c.history[name], sample{now, st})
	cut := 0
	for cut < len(h) && now.Sub(h[cut].at) > window {
		cut++
	}
	c.history[name] = h[cut:]
	c.latest[name] = Service{Name: name, State: st, LatencyMS: lat.Milliseconds(), CheckedAt: now}
}

// Snapshot builds the page data.
func (c *Checker) Snapshot() Page {
	now := c.now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	p := Page{Title: c.title, Overall: Up, UpdatedAt: now}
	for _, s := range c.services {
		svc, ok := c.latest[s.Name]
		if !ok {
			svc = Service{Name: s.Name, State: Unknown}
		}
		bars := make([]State, buckets)
		for i := range bars {
			bars[i] = Unknown
		}
		good, total := 0, 0
		start := now.Add(-window)
		for _, x := range c.history[s.Name] {
			i := int(x.at.Sub(start) / (window / buckets))
			if i < 0 || i >= buckets {
				if i == buckets {
					i = buckets - 1
				} else {
					continue
				}
			}
			if rank(x.state) > rank(bars[i]) {
				bars[i] = x.state
			}
			total++
			if x.state != Down {
				good++
			}
		}
		svc.Bars = bars
		svc.Uptime24h = -1
		if total > 0 {
			svc.Uptime24h = float64(good) * 100 / float64(total)
		}
		if rank(svc.State) > rank(p.Overall) {
			p.Overall = svc.State
		}
		p.Services = append(p.Services, svc)
	}
	return p
}

//go:embed page.html
var files embed.FS

var tmpl = template.Must(template.New("page.html").Funcs(template.FuncMap{
	"headline": func(s State) string {
		switch s {
		case Up:
			return "All systems operational"
		case Slow:
			return "Some services are slow"
		case Down:
			return "Some services are down"
		}
		return "Checking…"
	},
	"label": func(s State) string {
		return map[State]string{Up: "Operational", Slow: "Slow", Down: "Down", Unknown: "No data"}[s]
	},
	"pct": func(f float64) string {
		if f < 0 {
			return "–"
		}
		return template.HTMLEscapeString(trimPct(f))
	},
}).ParseFS(files, "page.html"))

func trimPct(f float64) string {
	return strconv.FormatFloat(math.Floor(f*100)/100, 'f', -1, 64) + "%"
}

// Handler serves the public page. Nothing else is reachable on this listener.
func (c *Checker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, c.Snapshot()); err != nil {
			c.log.Error("rendering status page", "err", err)
		}
	})
	mux.HandleFunc("GET /api/status.json", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(w).Encode(c.Snapshot())
	})
	return mux
}

func secure(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "public, max-age=30")
}
