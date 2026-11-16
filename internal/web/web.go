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

// Handler builds the internal mux. token may be empty (then the
// NetworkPolicy is the only guard).
func Handler(token string, onAlert AlertHandler, ready func() bool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/alertmanager", func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := r.Header.Get("Authorization")
			if subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
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
