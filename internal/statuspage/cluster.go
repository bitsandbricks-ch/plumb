package statuspage

import (
	"context"
	"fmt"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
)

// ClusterQuerier is the part of prom.Client the cluster summary needs.
type ClusterQuerier interface {
	Scalar(ctx context.Context, q string) (float64, error)
}

// AlertLister is the part of alertmanager.Client the cluster summary needs.
type AlertLister interface {
	ActiveAlerts(ctx context.Context) ([]alertmanager.ActiveAlert, error)
}

// Cluster builds the public cluster summary: counts and percentages only.
type Cluster struct {
	Q  ClusterQuerier
	AM AlertLister
}

const (
	cqNodesTotal = `count(kube_node_info)`
	cqNodesReady = `sum(kube_node_status_condition{condition="Ready",status="true"})`
	cqPods       = `sum(kube_pod_status_phase{phase="Running"})`
	cqCPU        = `1 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m]))`
	cqMemory     = `1 - sum(node_memory_MemAvailable_bytes) / sum(node_memory_MemTotal_bytes)`
)

func (c Cluster) alertCounts(ctx context.Context) (crit, warn int, err error) {
	alerts, err := c.AM.ActiveAlerts(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, a := range alerts {
		switch a.Labels["severity"] {
		case "critical":
			crit++
		case "warning":
			warn++
		}
	}
	return crit, warn, nil
}

// Check is the "Kubernetes cluster" row: down when no node is ready,
// degraded when a node is not ready or a critical alert is firing.
func (c Cluster) Check(ctx context.Context) State {
	total, err1 := c.Q.Scalar(ctx, cqNodesTotal)
	ready, err2 := c.Q.Scalar(ctx, cqNodesReady)
	if err1 != nil || err2 != nil || total == 0 {
		return Unknown
	}
	if ready == 0 {
		return Down
	}
	n, _, err := c.alertCounts(ctx)
	if ready < total || (err == nil && n > 0) {
		return Degraded
	}
	return Up
}

// Stats is the row of numbers under "Platform".
func (c Cluster) Stats(ctx context.Context) []Stat {
	pct := func(q string, warn, bad float64) Stat {
		v, err := c.Q.Scalar(ctx, q)
		if err != nil {
			return Stat{Value: "–", State: Unknown}
		}
		st := Up
		if v >= bad {
			st = Down
		} else if v >= warn {
			st = Degraded
		}
		return Stat{Value: fmt.Sprintf("%.0f%%", v*100), State: st}
	}
	var out []Stat

	nodes := Stat{Label: "Nodes ready", Value: "–", State: Unknown}
	total, err1 := c.Q.Scalar(ctx, cqNodesTotal)
	ready, err2 := c.Q.Scalar(ctx, cqNodesReady)
	if err1 == nil && err2 == nil && total > 0 {
		nodes.Value, nodes.State = fmt.Sprintf("%.0f / %.0f", ready, total), Up
		if ready < total {
			nodes.State = Degraded
		}
	}
	out = append(out, nodes)

	pods := Stat{Label: "Pods running", Value: "–", State: Unknown}
	if v, err := c.Q.Scalar(ctx, cqPods); err == nil {
		pods.Value, pods.State = fmt.Sprintf("%.0f", v), Up
	}
	out = append(out, pods)

	cpu := pct(cqCPU, 0.8, 0.95)
	cpu.Label = "CPU in use"
	mem := pct(cqMemory, 0.85, 0.95)
	mem.Label = "Memory in use"
	out = append(out, cpu, mem)

	al := Stat{Label: "Active alerts", Value: "–", State: Unknown}
	if crit, warn, err := c.alertCounts(ctx); err == nil {
		switch {
		case crit > 0:
			al.Value, al.State = fmt.Sprintf("%d critical, %d warning", crit, warn), Down
		case warn > 0:
			al.Value, al.State = fmt.Sprintf("%d warning", warn), Degraded
		default:
			al.Value, al.State = "none", Up
		}
	}
	return append(out, al)
}
