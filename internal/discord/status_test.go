package discord

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
	"github.com/bitsandbricks-ch/plumb/internal/prom"
)

type fakeQ map[string][]prom.Sample

func (f fakeQ) Query(_ context.Context, q string) ([]prom.Sample, error) {
	s, ok := f[q]
	if !ok {
		return nil, errors.New("no such query")
	}
	return s, nil
}

func (f fakeQ) Scalar(ctx context.Context, q string) (float64, error) {
	s, err := f.Query(ctx, q)
	if err != nil || len(s) == 0 {
		return 0, err
	}
	return s[0].Value, nil
}

type fakeAM []alertmanager.ActiveAlert

func (f fakeAM) ActiveAlerts(context.Context) ([]alertmanager.ActiveAlert, error) { return f, nil }

func v(x float64, kv ...string) prom.Sample {
	l := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		l[kv[i]] = kv[i+1]
	}
	return prom.Sample{Labels: l, Value: x}
}

func healthy() fakeQ {
	return fakeQ{
		qNodesTotal: {v(5)}, qNodesReady: {v(5)},
		qBadPods: nil, qStuckPods: nil, qShortDeps: nil, qLonghorn: nil,
		qPostgres: {v(0.31, "namespace", "demo")},
		qDisk:     {v(0.42, "nodename", "node1")},
	}
}

func TestStatusAllGood(t *testing.T) {
	am := fakeAM{{Labels: map[string]string{"alertname": "Watchdog", "severity": "none"}}}
	e := StatusEmbed(context.Background(), healthy(), am, "")
	if e.Title != "Oak: all good" || e.Color != colorResolved {
		t.Fatalf("%q\n%s", e.Title, e.Description)
	}
	for _, want := range []string{"5 of 5 ready", "none firing", "demo 31%", "node1 42%"} {
		if !strings.Contains(e.Description, want) {
			t.Errorf("missing %q in\n%s", want, e.Description)
		}
	}
}

func TestStatusBroken(t *testing.T) {
	q := healthy()
	q[qNodesReady] = []prom.Sample{v(4)}
	q[qBadPods] = []prom.Sample{v(1, "namespace", "floorbit", "pod", "web-1", "reason", "CrashLoopBackOff")}
	delete(q, qLonghorn) // query failure shows as unknown, not as a crash
	am := fakeAM{{Labels: map[string]string{"alertname": "PodCrashLooping", "severity": "critical"}}}
	e := StatusEmbed(context.Background(), q, am, "https://grafana.example")
	if e.Title != "Oak: something is broken" || e.URL == "" {
		t.Fatalf("%q", e.Title)
	}
	for _, want := range []string{"4 of 5 ready", "floorbit/web-1 (CrashLoopBackOff)", "1 critical", "❔ **Longhorn**"} {
		if !strings.Contains(e.Description, want) {
			t.Errorf("missing %q in\n%s", want, e.Description)
		}
	}
}
