// Package web is Plumb's internal HTTP listener: the Alertmanager webhook
// and health checks. It must never be exposed outside the cluster.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
)

// AlertHandler receives one Alertmanager group notification.
type AlertHandler func(ctx context.Context, w alertmanager.Webhook) error

// Handler builds the internal mux. Every webhook call must carry token; an
// empty token rejects them all.
func Handler(token string, onAlert AlertHandler, ready func() bool, metrics http.Handler, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	if metrics != nil {
		// Scraped by VictoriaMetrics: the status page history.
		mux.Handle("GET /metrics", metrics)
	}
	mux.HandleFunc("POST /webhook/alertmanager", func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var payload alertmanager.Webhook
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := onAlert(ctx, payload); err != nil {
			// 5xx makes Alertmanager retry.
			log.Error("handling alert failed", "group", payload.GroupKey, "err", err)
			http.Error(w, "failed", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready() {
			http.Error(w, "discord not connected", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}
