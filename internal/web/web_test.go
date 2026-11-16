package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
)

func TestWebhook(t *testing.T) {
	var got []alertmanager.Webhook
	fail := false
	h := Handler("s3cret", func(_ context.Context, w alertmanager.Webhook) error {
		if fail {
			return errors.New("discord down")
		}
		got = append(got, w)
		return nil
	}, func() bool { return false }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	do := func(auth, body string) int {
		r := httptest.NewRequest("POST", "/webhook/alertmanager", strings.NewReader(body))
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	body := `{"status":"firing","groupKey":"k","alerts":[{"status":"firing","labels":{"alertname":"X"}}]}`
	if c := do("", body); c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := do("Bearer nope", body); c != 401 {
		t.Fatalf("wrong token: %d", c)
	}
	if c := do("Bearer s3cret", "{"); c != 400 {
		t.Fatalf("bad json: %d", c)
	}
	if c := do("Bearer s3cret", body); c != 200 || len(got) != 1 || got[0].Alerts[0].Labels["alertname"] != "X" {
		t.Fatalf("ok: %d %+v", c, got)
	}
	fail = true
	if c := do("Bearer s3cret", body); c != 502 {
		t.Fatalf("failure should ask for retry: %d", c)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 {
		t.Fatalf("readyz %d", rec.Code)
	}
}
