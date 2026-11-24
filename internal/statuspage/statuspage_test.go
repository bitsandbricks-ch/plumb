package statuspage

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
	"github.com/bitsandbricks-ch/plumb/internal/prom"
)

var zurich, _ = time.LoadLocation("Europe/Zurich")

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestProbesAndPage(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer login.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
	defer bad.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(60 * time.Millisecond) }))
	defer slow.Close()

	c := NewChecker([]Target{
		{Name: "Seatbit", URL: ok.URL}, {Name: "Floorbit", URL: bad.URL}, {Name: "Hubra", URL: slow.URL},
		{Name: "Mason", URL: login.URL}, {Name: "<b>New</b>", URL: ok.URL},
	}, Options{Title: "Test status", Host: "status.example", Slow: 50 * time.Millisecond, Location: zurich, Log: quiet()})
	c.AddComponent(Component{Name: "Discord bot", Check: func(context.Context) State { return Up }})
	c.SetStats(func(context.Context) []Stat { return []Stat{{Label: "Pods running", Value: "42", State: Up}} })
	c.CheckAll(context.Background())

	p := c.Snapshot()
	want := map[string]State{"Seatbit": Up, "Floorbit": Down, "Hubra": Degraded, "Mason": Up, "Discord bot": Up}
	for _, g := range p.Groups {
		for _, r := range g.Rows {
			if w, ok := want[r.Name]; ok && r.State != w {
				t.Errorf("%s: got %s want %s", r.Name, r.State, w)
			}
			if len(r.Bars) != historyDays {
				t.Errorf("%s: %d bars", r.Name, len(r.Bars))
			}
		}
	}
	if len(p.Groups) != 2 || p.Groups[0].Name != "Apps" || p.Groups[1].Name != "Platform" {
		t.Fatalf("groups %+v", p.Groups)
	}
	if p.Overall != Down || p.Headline != "Some systems are down" {
		t.Fatalf("overall %s", p.Overall)
	}
	// Today's bar falls back to the live state before VictoriaMetrics has data.
	if b := p.Groups[0].Rows[0].Bars; b[len(b)-1].State == Unknown || b[0].State != Unknown {
		t.Fatalf("bars %v %v", b[0], b[len(b)-1])
	}

	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	for _, w := range []string{"Some systems are down", "Pods running", "No incidents in the last 14 days", "status.example", "Discord bot"} {
		if !strings.Contains(body, w) {
			t.Errorf("page lacks %q", w)
		}
	}
	if strings.Contains(body, "<b>New</b>") || strings.Contains(body, ok.URL) {
		t.Fatal("names must be escaped and URLs never shown")
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("missing CSP")
	}

	for path, code := range map[string]int{"/webhook/alertmanager": 404, "/healthz": 404, "/metrics": 404, "/assets/favicon.svg": 200, "/assets/../page.html": 404} {
		rec = httptest.NewRecorder()
		c.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != code && !(code == 404 && rec.Code/100 == 3) {
			t.Errorf("%s: got %d want %d", path, rec.Code, code)
		}
	}
	rec = httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/status.json", nil))
	if !strings.Contains(rec.Body.String(), `"overall":"down"`) || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("json %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	c.MetricsHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	m := rec.Body.String()
	for _, w := range []string{`plumb_status_state{service="Floorbit",group="Apps"} 0`, `plumb_status_state{service="Hubra",group="Apps"} 1`, `plumb_status_state{service="Discord bot",group="Platform"} 2`, `service="\u003cb\u003eNew\u003c/b\u003e"`} {
		if w == `service="\u003cb\u003eNew\u003c/b\u003e"` {
			w = `service="<b>New</b>"`
		}
		if !strings.Contains(m, w) {
			t.Errorf("metrics lack %q:\n%s", w, m)
		}
	}
}

func TestNoticesAndMaintenance(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "notices.yaml")
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, zurich)
	os.WriteFile(f, []byte(`
notices:
  - title: Database upgrade
    kind: maintenance
    services: [Floorbit]
    start: 2026-11-16T14:00:00+01:00
    end: 2026-11-16T16:00:00+01:00
    body: Short downtime expected.
  - title: Next week's node swap
    kind: maintenance
    start: 2026-11-20T20:00:00+01:00
    end: 2026-11-20T21:00:00+01:00
  - title: Seatbit login errors
    kind: incident
    services: [Seatbit]
    start: 2026-11-14T10:00:00+01:00
    end: 2026-11-14T10:40:00+01:00
    body: A bad config was rolled back.
  - title: Long ago
    kind: incident
    start: 2026-09-01T10:00:00+02:00
    end: 2026-09-01T11:00:00+02:00
`), 0o644)
	c := NewChecker([]Target{{Name: "Floorbit"}, {Name: "Seatbit"}}, Options{Title: "t", Location: zurich, NoticesFile: f, Log: quiet()})
	c.now = func() time.Time { return now }
	c.Refresh(context.Background())
	c.record("Floorbit", Down, 0)
	c.record("Seatbit", Up, 0)
	c.history.Incidents = []Incident{
		{Service: "Seatbit", Kind: Down, Start: time.Date(2026, 11, 14, 10, 2, 0, 0, zurich), End: time.Date(2026, 11, 14, 10, 30, 0, 0, zurich)},
		{Service: "Seatbit", Kind: Degraded, Start: time.Date(2026, 11, 15, 9, 0, 0, 0, zurich), End: time.Date(2026, 11, 15, 9, 20, 0, 0, zurich)},
	}
	p := c.Snapshot()
	if p.Groups[0].Rows[0].State != Maintenance || p.Overall != Maintenance || p.Headline != "Maintenance in progress" {
		t.Fatalf("maintenance override: %+v overall %s", p.Groups[0].Rows[0].State, p.Overall)
	}
	if len(p.Active) != 2 || !p.Active[0].InProgress || p.Active[1].InProgress || !strings.HasPrefix(p.Active[1].When, "Fri 20 Nov") {
		t.Fatalf("active %+v", p.Active)
	}
	if len(p.Past) != 2 || p.Past[0].Items[0].Title != "Seatbit was degraded" || p.Past[1].Items[0].Title != "Seatbit login errors" {
		t.Fatalf("past %+v", p.Past)
	}
	if d := p.Past[1].Items[0].Detected; len(d) != 1 || !strings.Contains(d[0], "Seatbit down") || !strings.Contains(d[0], "(28 min)") {
		t.Fatalf("detected not attached: %+v", d)
	}
	if !strings.Contains(p.Past[1].Items[0].When, "10:00 – 10:40 (40 min)") {
		t.Fatalf("when %q", p.Past[1].Items[0].When)
	}

	os.WriteFile(f, []byte("notices:\n  - title: x\n    kind: party\n    start: 2026-11-16T10:00:00Z\n"), 0o644)
	if _, err := LoadNotices(f); err == nil {
		t.Fatal("bad kind must be rejected")
	}
	if ns, err := LoadNotices(filepath.Join(dir, "missing.yaml")); err != nil || ns != nil {
		t.Fatal("a missing file is no notices, not an error")
	}
}

func TestDetect(t *testing.T) {
	base := time.Unix(1700000000, 0)
	var pts []prom.Point
	add := func(v float64, n int) {
		for i := 0; i < n; i++ {
			pts = append(pts, prom.Point{T: base.Add(time.Duration(len(pts)) * incidentStep), V: v})
		}
	}
	add(2, 5)
	add(0, 1) // single blip: ignored
	add(2, 5)
	add(0, 3) // outage: 6 min
	add(2, 5)
	add(1, 4) // short slowness: ignored
	add(2, 2)
	add(1, 6) // long slowness: kept
	add(2, 3)
	add(0, 4) // still going at the end: not "past"
	now := pts[len(pts)-1].T.Add(time.Minute)
	inc := detect("A", pts, incidentStep, now)
	if len(inc) != 2 || inc[0].Kind != Down || inc[0].End.Sub(inc[0].Start) != 6*time.Minute || inc[1].Kind != Degraded {
		t.Fatalf("%+v", inc)
	}
}

type fakeRange struct{}

func (fakeRange) Query(_ context.Context, q string) ([]prom.Sample, error) {
	return []prom.Sample{{Labels: map[string]string{"service": "A"}, Value: 1}}, nil
}
func (fakeRange) QueryRange(_ context.Context, q string, start, end time.Time, step time.Duration) ([]prom.Series, error) {
	if step == 24*time.Hour {
		return []prom.Series{{Labels: map[string]string{"service": "A"}, Points: []prom.Point{{T: end, V: 0.97}, {T: end.Add(-24 * time.Hour), V: 1}}}}, nil
	}
	return nil, nil
}

func TestLoadHistory(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, zurich)
	h, err := LoadHistory(context.Background(), fakeRange{}, now, zurich)
	if err != nil {
		t.Fatal(err)
	}
	row := h.Daily["A"]
	if len(h.Days) != 90 || row[89] != 1 || row[88] != 0.97 || row[87] != 1 || row[0] != -1 {
		t.Fatalf("days %d last %v", len(h.Days), row[85:])
	}
}

func TestDiscover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("labelSelector") != "status.reyki.me/show=true" {
			t.Errorf("bad request %v %s", r.Header, r.URL.RawQuery)
		}
		w.Write([]byte(`{"items":[
		 {"metadata":{"name":"floorbit-public","annotations":{"reyki.me/name":"Floorbit"}},"spec":{"routes":[{"match":"Host(` + "`floor.example.com`" + `) && PathPrefix(` + "`/api/`" + `)"}]}},
		 {"metadata":{"name":"x","annotations":{"status.reyki.me/name":"Hubra","status.reyki.me/url":"https://app.example.com/health","status.reyki.me/group":"Hubra","status.reyki.me/order":"5"}},"spec":{"routes":[]}},
		 {"metadata":{"name":"nohost"},"spec":{"routes":[{"match":"PathPrefix(` + "`/`" + `)"}]}}]}`))
	}))
	defer srv.Close()
	ts, err := (&Discoverer{BaseURL: srv.URL, Token: "tok", HTTP: srv.Client()}).Discover(context.Background())
	if err != nil || len(ts) != 2 {
		t.Fatalf("%+v %v", ts, err)
	}
	if ts[0].Name != "Floorbit" || ts[0].URL != "https://floor.example.com/" || ts[0].Group != "Apps" {
		t.Fatalf("%+v", ts[0])
	}
	if ts[1].Group != "Hubra" || ts[1].URL != "https://app.example.com/health" || ts[1].Order != 5 {
		t.Fatalf("%+v", ts[1])
	}
	// Static targets win over discovered ones with the same name.
	c := NewChecker([]Target{{Name: "Floorbit", URL: "https://static/"}}, Options{Log: quiet()})
	c.discovered = ts
	got := c.Targets()
	if len(got) != 2 || got[0].URL != "https://static/" {
		t.Fatalf("%+v", got)
	}
}

type fakeScalar map[string]float64

func (f fakeScalar) Scalar(_ context.Context, q string) (float64, error) { return f[q], nil }

type fakeAlerts []alertmanager.ActiveAlert

func (f fakeAlerts) ActiveAlerts(context.Context) ([]alertmanager.ActiveAlert, error) { return f, nil }

func TestCluster(t *testing.T) {
	q := fakeScalar{cqNodesTotal: 5, cqNodesReady: 5, cqPods: 120, cqCPU: 0.23, cqMemory: 0.9}
	c := Cluster{Q: q, AM: fakeAlerts{{Labels: map[string]string{"alertname": "Watchdog", "severity": "none"}}}}
	if s := c.Check(context.Background()); s != Up {
		t.Fatalf("check %s", s)
	}
	st := c.Stats(context.Background())
	got := map[string]Stat{}
	for _, s := range st {
		got[s.Label] = s
	}
	if got["Nodes ready"].Value != "5 / 5" || got["CPU in use"].Value != "23%" || got["Memory in use"].State != Degraded || got["Active alerts"].Value != "none" {
		t.Fatalf("%+v", st)
	}
	c.AM = fakeAlerts{{Labels: map[string]string{"alertname": "X", "severity": "critical"}}}
	if c.Check(context.Background()) != Degraded {
		t.Fatal("critical alert should degrade the cluster row")
	}
	for _, s := range c.Stats(context.Background()) {
		if strings.Contains(s.Value, "X") {
			t.Fatal("alert names must never be public")
		}
	}
}
