package alertmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateSilence(t *testing.T) {
	var got Silence
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/silences" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"silenceID":"abc"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL + "/")
	now := time.Now()
	id, err := c.CreateSilence(context.Background(), Silence{
		Matchers: []Matcher{{Name: "alertname", Value: "X", IsEqual: true}},
		StartsAt: now, EndsAt: now.Add(time.Hour), CreatedBy: "mike",
	})
	if err != nil || id != "abc" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if got.CreatedBy != "mike" || got.Matchers[0].Name != "alertname" || !got.Matchers[0].IsEqual {
		t.Fatalf("bad body %+v", got)
	}
}

func TestCreateSilenceRefusesEmptyMatchers(t *testing.T) {
	if _, err := NewClient("http://x").CreateSilence(context.Background(), Silence{}); err == nil {
		t.Fatal("a silence without matchers would mute everything; must refuse")
	}
}

func TestActiveAlertsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("silenced") != "false" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL).ActiveAlerts(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
