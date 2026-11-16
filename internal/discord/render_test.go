package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
)

func TestGroupFooterRoundTrip(t *testing.T) {
	in := map[string]string{"alertname": "PostgresConnectionsHigh", "namespace": `we"ird ns`}
	f := GroupFooter(in)
	if f != `group: alertname="PostgresConnectionsHigh" namespace="we\"ird ns"` {
		t.Fatalf("footer %q", f)
	}
	out, err := ParseGroupFooter(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out["namespace"] != `we"ird ns` {
		t.Fatalf("parsed %v", out)
	}
	if _, err := ParseGroupFooter("something else"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := ParseGroupFooter("group: "); err == nil {
		t.Fatal("empty group must not parse (would silence everything)")
	}
}

func webhook(status string, n int) alertmanager.Webhook {
	w := alertmanager.Webhook{
		Status:       status,
		GroupKey:     `{}:{alertname="PodCrashLooping", namespace="floorbit"}`,
		GroupLabels:  map[string]string{"alertname": "PodCrashLooping", "namespace": "floorbit"},
		CommonLabels: map[string]string{"alertname": "PodCrashLooping", "namespace": "floorbit", "severity": "critical"},
	}
	for i := 0; i < n; i++ {
		w.Alerts = append(w.Alerts, alertmanager.Alert{
			Status:       status,
			Labels:       map[string]string{"alertname": "PodCrashLooping", "namespace": "floorbit", "pod": "web-" + string(rune('a'+i))},
			Annotations:  map[string]string{"summary": "Pod is crash looping", "description": "restarted 5 times"},
			StartsAt:     time.Unix(1700000000+int64(i), 0),
			GeneratorURL: "https://alerts.example/vmalert/alert?group=1",
		})
	}
	return w
}

func TestAlertEmbedFiring(t *testing.T) {
	e := AlertEmbed(webhook("firing", 10))
	if e.Title != "Firing · PodCrashLooping (10)" || e.Color != colorCritical {
		t.Fatalf("title %q color %x", e.Title, e.Color)
	}
	if !strings.Contains(e.Description, "pod=web-a") || !strings.Contains(e.Description, "…and 2 more") {
		t.Fatalf("description %q", e.Description)
	}
	if strings.Contains(e.Description, "pod=web-j") {
		t.Fatal("should cap shown alerts")
	}
	if !strings.Contains(e.Fields[2].Value, "<t:1700000000:R>") {
		t.Fatalf("since %q", e.Fields[2].Value)
	}
	if e.URL == "" {
		t.Fatal("missing link")
	}
	if _, err := ParseGroupFooter(e.Footer.Text); err != nil {
		t.Fatal(err)
	}
}

func TestAlertEmbedResolved(t *testing.T) {
	e := AlertEmbed(webhook("resolved", 1))
	if e.Title != "Resolved · PodCrashLooping" || e.Color != colorResolved {
		t.Fatalf("title %q color %x", e.Title, e.Color)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("äbcdef", 4); got != "äbc…" {
		t.Fatalf("%q", got)
	}
}
