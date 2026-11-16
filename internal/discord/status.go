package discord

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
	"github.com/bitsandbricks-ch/plumb/internal/prom"
)

// PromQL used by /status. Everything comes from kube-state-metrics,
// node-exporter, CNPG and Longhorn metrics already in VictoriaMetrics, so
// Plumb needs no Kubernetes API access at all for v1.
const (
	qNodesTotal = `count(kube_node_info)`
	qNodesReady = `sum(kube_node_status_condition{condition="Ready",status="true"})`
	qBadPods    = `max by (namespace, pod, reason) (kube_pod_container_status_waiting_reason{reason=~"CrashLoopBackOff|ImagePullBackOff|ErrImagePull|CreateContainerConfigError"}) == 1`
	qStuckPods  = `sum by (namespace, pod, phase) (kube_pod_status_phase{phase=~"Pending|Failed|Unknown"}) > 0`
	qShortDeps  = `kube_deployment_spec_replicas > kube_deployment_status_replicas_available`
	qLonghorn   = `longhorn_volume_robustness > 1`
	qPostgres   = `max by (namespace) (sum by (namespace, pod) (cnpg_backends_total) / on (namespace, pod) max by (namespace, pod) (cnpg_pg_settings_setting{name="max_connections"}))`
	qDisk       = `topk(3, max by (nodename) ((1 - node_filesystem_avail_bytes{fstype!~"tmpfs|overlay|squashfs|nsfs"} / node_filesystem_size_bytes{fstype!~"tmpfs|overlay|squashfs|nsfs"}) * on (instance) group_left (nodename) node_uname_info))`
)

// Querier is the part of prom.Client that /status needs.
type Querier interface {
	Query(ctx context.Context, q string) ([]prom.Sample, error)
	Scalar(ctx context.Context, q string) (float64, error)
}

// AlertLister is the part of alertmanager.Client that /status needs.
type AlertLister interface {
	ActiveAlerts(ctx context.Context) ([]alertmanager.ActiveAlert, error)
}

type line struct {
	name  string
	level int // 0 ok, 1 warning, 2 critical, -1 unknown
	text  string
}

// StatusEmbed builds the /status summary. Each check is independent: one
// failing query shows as "unknown" and doesn't hide the rest.
func StatusEmbed(ctx context.Context, q Querier, am AlertLister, grafanaURL string) *discordgo.MessageEmbed {
	checks := []func(context.Context) line{
		func(ctx context.Context) line { return nodesLine(ctx, q) },
		func(ctx context.Context) line { return alertsLine(ctx, am) },
		func(ctx context.Context) line {
			return listLine(ctx, q, "Pods", qBadPods, qStuckPods, 2, func(l map[string]string) string {
				r := l["reason"]
				if r == "" {
					r = l["phase"]
				}
				return fmt.Sprintf("%s/%s (%s)", l["namespace"], l["pod"], r)
			})
		},
		func(ctx context.Context) line {
			return listLine(ctx, q, "Deployments", qShortDeps, "", 1, func(l map[string]string) string {
				return l["namespace"] + "/" + l["deployment"] + " is missing replicas"
			})
		},
		func(ctx context.Context) line {
			return listLine(ctx, q, "Longhorn", qLonghorn, "", 1, func(l map[string]string) string {
				name := l["volume"]
				if pvc := l["pvc"]; pvc != "" {
					name = l["pvc_namespace"] + "/" + pvc
				}
				return name + " degraded"
			})
		},
		func(ctx context.Context) line { return postgresLine(ctx, q) },
		func(ctx context.Context) line { return diskLine(ctx, q) },
	}

	lines := make([]line, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func() { defer wg.Done(); lines[i] = c(ctx) }()
	}
	wg.Wait()

	worst := 0
	var b strings.Builder
	for _, l := range lines {
		icon := map[int]string{-1: "❔", 0: "🟢", 1: "🟠", 2: "🔴"}[l.level]
		fmt.Fprintf(&b, "%s **%s**: %s\n", icon, l.name, l.text)
		if l.level > worst {
			worst = l.level
		}
	}
	title := map[int]string{0: "Oak: all good", 1: "Oak: needs attention", 2: "Oak: something is broken"}[worst]
	e := &discordgo.MessageEmbed{
		Title:       title,
		Description: truncate(b.String(), 4000),
		Color:       map[int]int{0: colorResolved, 1: colorWarning, 2: colorCritical}[worst],
	}
	if grafanaURL != "" {
		e.URL = grafanaURL
	}
	return e
}

func nodesLine(ctx context.Context, q Querier) line {
	total, err1 := q.Scalar(ctx, qNodesTotal)
	ready, err2 := q.Scalar(ctx, qNodesReady)
	if err1 != nil || err2 != nil || total == 0 {
		return line{"Nodes", -1, "unknown (metrics query failed)"}
	}
	lvl := 0
	if ready < total {
		lvl = 2
	}
	return line{"Nodes", lvl, fmt.Sprintf("%.0f of %.0f ready", ready, total)}
}

func alertsLine(ctx context.Context, am AlertLister) line {
	alerts, err := am.ActiveAlerts(ctx)
	if err != nil {
		return line{"Alerts", -1, "unknown (Alertmanager not reachable)"}
	}
	count := map[string]int{}
	for _, a := range alerts {
		n := a.Labels["alertname"]
		if n == "Watchdog" || n == "InfoInhibitor" {
			continue
		}
		count[a.Labels["severity"]]++
	}
	switch {
	case count["critical"] > 0:
		return line{"Alerts", 2, fmt.Sprintf("%d critical, %d warning firing", count["critical"], count["warning"])}
	case count["warning"] > 0:
		return line{"Alerts", 1, fmt.Sprintf("%d warning firing", count["warning"])}
	}
	return line{"Alerts", 0, "none firing"}
}

func listLine(ctx context.Context, q Querier, name, q1, q2 string, level int, f func(map[string]string) string) line {
	var items []string
	for _, query := range []string{q1, q2} {
		if query == "" {
			continue
		}
		s, err := q.Query(ctx, query)
		if err != nil {
			return line{name, -1, "unknown (metrics query failed)"}
		}
		for _, x := range s {
			items = append(items, f(x.Labels))
		}
	}
	if len(items) == 0 {
		return line{name, 0, "all fine"}
	}
	sort.Strings(items)
	extra := ""
	if len(items) > 5 {
		extra = fmt.Sprintf(", and %d more", len(items)-5)
		items = items[:5]
	}
	return line{name, level, strings.Join(items, ", ") + extra}
}

func postgresLine(ctx context.Context, q Querier) line {
	s, err := q.Query(ctx, qPostgres)
	if err != nil {
		return line{"Postgres", -1, "unknown (metrics query failed)"}
	}
	if len(s) == 0 {
		return line{"Postgres", -1, "no CNPG metrics"}
	}
	sort.Slice(s, func(i, j int) bool { return s[i].Value > s[j].Value })
	lvl := 0
	var parts []string
	for _, x := range s {
		if x.Value > 0.8 {
			lvl = 1
		}
		parts = append(parts, fmt.Sprintf("%s %.0f%%", x.Labels["namespace"], x.Value*100))
	}
	return line{"Postgres connections", lvl, strings.Join(parts, ", ")}
}

func diskLine(ctx context.Context, q Querier) line {
	s, err := q.Query(ctx, qDisk)
	if err != nil || len(s) == 0 {
		return line{"Disk", -1, "unknown (metrics query failed)"}
	}
	sort.Slice(s, func(i, j int) bool { return s[i].Value > s[j].Value })
	lvl := 0
	var parts []string
	for _, x := range s {
		switch {
		case x.Value > 0.9:
			lvl = 2
		case x.Value > 0.8 && lvl < 1:
			lvl = 1
		}
		parts = append(parts, fmt.Sprintf("%s %.0f%%", x.Labels["nodename"], x.Value*100))
	}
	return line{"Disk (fullest)", lvl, strings.Join(parts, ", ")}
}
