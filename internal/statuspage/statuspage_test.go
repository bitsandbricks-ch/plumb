package statuspage

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bitsandbricks-ch/plumb/internal/config"
)

func TestCheckAndRender(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
	defer bad.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(60 * time.Millisecond) }))
	defer slow.Close()

	c := NewChecker("Test status", []config.Service{
		{Name: "Seatbit", URL: ok.URL}, {Name: "Floorbit", URL: bad.URL}, {Name: "Hubra", URL: slow.URL}, {Name: "<b>New</b>", URL: ok.URL},
	}, 50*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.CheckAll(context.Background())

	p := c.Snapshot()
	want := map[string]State{"Seatbit": Up, "Floorbit": Down, "Hubra": Slow}
	for _, s := range p.Services {
		if w, ok := want[s.Name]; ok && s.State != w {
			t.Errorf("%s: got %s want %s", s.Name, s.State, w)
		}
	}
	if p.Overall != Down {
		t.Fatalf("overall %s", p.Overall)
	}
	if p.Services[0].Uptime24h != 100 || p.Services[1].Uptime24h != 0 || p.Services[0].Bars[buckets-1] != Up {
		t.Fatalf("history wrong: %+v", p.Services[:2])
	}

	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Some services are down") || !strings.Contains(body, "100%") {
		t.Fatalf("page %d:\n%s", rec.Code, body)
	}
	if strings.Contains(body, "<b>New</b>") || strings.Contains(body, ok.URL) {
		t.Fatal("names must be escaped and URLs never shown")
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}

	for _, path := range []string{"/webhook/alertmanager", "/healthz", "/foo"} {
		rec = httptest.NewRecorder()
		c.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 404 {
			t.Errorf("%s: public listener must only serve the page, got %d", path, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/status.json", nil))
	if !strings.Contains(rec.Body.String(), `"overall":"down"`) {
		t.Fatalf("json %s", rec.Body.String())
	}
}

func TestHistoryWindow(t *testing.T) {
	c := NewChecker("x", []config.Service{{Name: "A"}}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := time.Unix(1700000000, 0)
	c.now = func() time.Time { return base }
	c.record("A", Down, 0)
	c.now = func() time.Time { return base.Add(25 * time.Hour) }
	c.record("A", Up, 0)
	p := c.Snapshot()
	if p.Services[0].Uptime24h != 100 || len(c.history["A"]) != 1 {
		t.Fatalf("old sample not dropped: %+v", p.Services[0])
	}
}
