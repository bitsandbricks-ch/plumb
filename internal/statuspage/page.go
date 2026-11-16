package statuspage

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// Bar is one day of history.
type Bar struct {
	Date         string  `json:"date"`
	State        State   `json:"state"`
	Availability float64 `json:"availability"` // 0..1, -1 = no data
}

// Row is one service or component.
type Row struct {
	Name     string  `json:"name"`
	State    State   `json:"state"`
	Uptime90 float64 `json:"uptime_90d"` // percent, -1 = no data
	Bars     []Bar   `json:"bars"`
}

// Group is a titled list of rows ("Apps", "Platform").
type Group struct {
	Name string `json:"name"`
	Rows []Row  `json:"rows"`
}

// NoticeView is a notice ready to show.
type NoticeView struct {
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	Body       string   `json:"body,omitempty"`
	When       string   `json:"when"`
	Services   []string `json:"services,omitempty"`
	InProgress bool     `json:"in_progress"`
}

// PastItem is one entry under "Past incidents".
type PastItem struct {
	Title    string   `json:"title"`
	Kind     string   `json:"kind"`
	When     string   `json:"when"`
	Body     string   `json:"body,omitempty"`
	Detected []string `json:"detected,omitempty"`
}

// PastDay groups past items by day.
type PastDay struct {
	Date  string     `json:"date"`
	Items []PastItem `json:"items"`
}

// Page is everything the template and the JSON endpoint show.
type Page struct {
	Title     string       `json:"title"`
	Host      string       `json:"-"`
	Overall   State        `json:"overall"`
	Headline  string       `json:"headline"`
	Active    []NoticeView `json:"notices"`
	Groups    []Group      `json:"groups"`
	Stats     []Stat       `json:"cluster"`
	Past      []PastDay    `json:"past_incidents"`
	Updated   string       `json:"-"`
	UpdatedAt time.Time    `json:"updated_at"`
}

func classify(avail float64) State {
	switch {
	case avail < 0:
		return Unknown
	case avail >= 0.999:
		return Up
	case avail >= 0.95:
		return Degraded
	}
	return Down
}

func (c *Checker) row(name string, now time.Time) Row {
	loc := c.opt.Location
	r := Row{Name: name, State: Unknown, Uptime90: -1}
	if res, ok := c.latest[name]; ok {
		r.State = res.state
	}
	for _, n := range c.notices {
		if n.Kind == "maintenance" && n.inProgress(now) && n.covers(name) && (r.State == Down || r.State == Degraded) {
			r.State = Maintenance
		}
	}
	days := c.history.Days
	if len(days) == 0 {
		today := midnight(now, loc)
		for i := historyDays - 1; i >= 0; i-- {
			days = append(days, today.AddDate(0, 0, -i))
		}
	}
	daily := c.history.Daily[name]
	sum, cnt := 0.0, 0
	for i, d := range days {
		a := -1.0
		if daily != nil {
			a = daily[i]
		}
		st := classify(a)
		if i == len(days)-1 && a < 0 && r.State != Unknown {
			st = r.State // today, before VictoriaMetrics has anything
		}
		for _, n := range c.notices {
			if n.Kind == "maintenance" && n.covers(name) && n.overlaps(d, d.AddDate(0, 0, 1)) && st != Up && st != Unknown {
				st = Maintenance
			}
		}
		if a >= 0 {
			sum += a
			cnt++
		}
		r.Bars = append(r.Bars, Bar{Date: d.Format("Mon 2 Jan"), State: st, Availability: a})
	}
	if cnt > 0 {
		r.Uptime90 = sum / float64(cnt) * 100
	}
	return r
}

// Snapshot builds the page data.
func (c *Checker) Snapshot() Page {
	now := c.now()
	loc := c.opt.Location
	targets := c.Targets()
	c.mu.RLock()
	defer c.mu.RUnlock()

	p := Page{Title: c.opt.Title, Host: c.opt.Host, Overall: Unknown, UpdatedAt: now,
		Updated: now.In(loc).Format("15:04 MST"), Stats: c.latestStats}

	var groups []Group
	idx := map[string]int{}
	for _, t := range targets {
		i, ok := idx[t.Group]
		if !ok {
			i = len(groups)
			idx[t.Group] = i
			groups = append(groups, Group{Name: t.Group})
		}
		groups[i].Rows = append(groups[i].Rows, c.row(t.Name, now))
	}
	if len(c.components) > 0 {
		g := Group{Name: "Platform"}
		for _, comp := range c.components {
			g.Rows = append(g.Rows, c.row(comp.Name, now))
		}
		groups = append(groups, g)
	}
	p.Groups = groups

	for _, g := range groups {
		for _, r := range g.Rows {
			if rank(r.State) > rank(p.Overall) {
				p.Overall = r.State
			}
		}
	}

	// Notices.
	active := []Notice{}
	for _, n := range c.notices {
		if n.active(now) {
			active = append(active, n)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Start.Before(active[j].Start) })
	for _, n := range active {
		if n.Kind == "incident" && rank(p.Overall) < rank(Degraded) {
			p.Overall = Degraded
		}
		p.Active = append(p.Active, NoticeView{
			Title: n.Title, Kind: n.Kind, Body: n.Body, Services: n.Services,
			When: span(n.Start, n.End, now, loc), InProgress: n.inProgress(now),
		})
	}
	p.Headline = headline(p.Overall)
	p.Past = c.past(now)
	return p
}

func headline(s State) string {
	switch s {
	case Up:
		return "All systems operational"
	case Maintenance:
		return "Maintenance in progress"
	case Degraded:
		return "Some systems are degraded"
	case Down:
		return "Some systems are down"
	}
	return "Checking…"
}

// past merges written notices and detected outages of the last 14 days.
func (c *Checker) past(now time.Time) []PastDay {
	loc := c.opt.Location
	since := now.AddDate(0, 0, -incidentDays)
	type item struct {
		start time.Time
		PastItem
	}
	var items []item
	var written []int // indexes into items of written incidents
	var notes []Notice
	for _, n := range c.notices {
		if n.Kind == "info" || n.End.IsZero() || n.End.After(now) || n.End.Before(since) {
			continue
		}
		items = append(items, item{n.Start, PastItem{Title: n.Title, Kind: n.Kind, Body: n.Body, When: span(n.Start, n.End, n.Start, loc)}})
		written = append(written, len(items)-1)
		notes = append(notes, n)
	}
	for _, inc := range c.history.Incidents {
		if inc.End.Before(since) {
			continue
		}
		what := "down"
		if inc.Kind == Degraded {
			what = "degraded"
		}
		line := fmt.Sprintf("%s %s %s", inc.Service, what, span(inc.Start, inc.End, inc.Start, loc))
		attached := false
		for k, n := range notes {
			if (len(n.Services) == 0 || n.covers(inc.Service)) && n.overlaps(inc.Start, inc.End) {
				it := &items[written[k]]
				it.Detected = append(it.Detected, line)
				attached = true
				break
			}
		}
		if !attached {
			items = append(items, item{inc.Start, PastItem{
				Title: fmt.Sprintf("%s was %s", inc.Service, what), Kind: "detected",
				When: span(inc.Start, inc.End, inc.Start, loc),
			}})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].start.After(items[j].start) })
	var out []PastDay
	for _, it := range items {
		d := it.start.In(loc).Format("Monday, 2 January")
		if len(out) == 0 || out[len(out)-1].Date != d {
			out = append(out, PastDay{Date: d})
		}
		out[len(out)-1].Items = append(out[len(out)-1].Items, it.PastItem)
	}
	return out
}

// span formats a time range in the page's time zone. The date is left out
// when start is on the same day as ref (today, or the day it's listed under).
func span(start, end, ref time.Time, loc *time.Location) string {
	s := start.In(loc)
	sameDay := func(a, b time.Time) bool { return a.In(loc).Format(time.DateOnly) == b.In(loc).Format(time.DateOnly) }
	day := ""
	if !sameDay(s, ref) {
		day = s.Format("Mon 2 Jan") + ", "
	}
	if end.IsZero() {
		if s.After(ref) {
			return "from " + day + s.Format("15:04")
		}
		return "since " + day + s.Format("15:04")
	}
	e := end.In(loc)
	to := e.Format("15:04")
	if !sameDay(s, e) {
		to = e.Format("Mon 2 Jan, 15:04")
	}
	return fmt.Sprintf("%s%s – %s (%s)", day, s.Format("15:04"), to, duration(e.Sub(s)))
}

func duration(d time.Duration) string {
	m := int(math.Round(d.Minutes()))
	switch {
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m%60 == 0:
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

//go:embed page.html assets
var files embed.FS

var tmpl = template.Must(template.New("page.html").Funcs(template.FuncMap{
	"label": func(s State) string {
		return map[State]string{Up: "Operational", Degraded: "Degraded", Down: "Down", Maintenance: "Maintenance", Unknown: "No data"}[s]
	},
	"pct": func(f float64) string {
		if f < 0 {
			return "no data"
		}
		return strconv.FormatFloat(math.Floor(f*100)/100, 'f', -1, 64) + "% uptime"
	},
	"avail": func(f float64) string {
		if f < 0 {
			return "no data"
		}
		return strconv.FormatFloat(math.Floor(f*10000)/100, 'f', -1, 64) + "%"
	},
	"kindLabel": func(k string) string {
		return map[string]string{"maintenance": "Maintenance", "incident": "Incident", "info": "Notice", "detected": "Detected"}[k]
	},
}).ParseFS(files, "page.html"))

// Handler serves the public page. Nothing else is reachable on this listener.
func (c *Checker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, c.Snapshot()); err != nil {
			c.opt.Log.Error("rendering status page", "err", err)
		}
	})
	mux.HandleFunc("GET /api/status.json", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Content-Type", "application/json")
		// reyki.me reads this for its status badge.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(w).Encode(c.Snapshot())
	})
	assets, _ := fs.Sub(files, "assets")
	static := http.StripPrefix("/assets/", http.FileServerFS(assets))
	mux.HandleFunc("GET /assets/{file}", func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		static.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/assets/favicon.svg", http.StatusMovedPermanently)
	})
	return mux
}

func secure(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; font-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "public, max-age=30")
}
