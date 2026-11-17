package statuspage

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// MetricsHandler exports the latest check results for VictoriaMetrics to
// scrape (internal listener only). That scrape is what gives the page its
// 90-day history.
//
//	plumb_status_state{service,group}           2 up, 1 degraded, 0 down
//	plumb_status_latency_seconds{service,group} HTTP probes only
func (c *Checker) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		groups := map[string]string{}
		for _, t := range c.Targets() {
			groups[t.Name] = t.Group
		}
		for _, comp := range c.components {
			groups[comp.Name] = "Platform"
		}
		c.mu.RLock()
		names := make([]string, 0, len(c.latest))
		for n := range c.latest {
			if _, ok := groups[n]; ok {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		var b strings.Builder
		b.WriteString("# HELP plumb_status_state Status page state: 2 up, 1 degraded, 0 down.\n# TYPE plumb_status_state gauge\n")
		for _, n := range names {
			if v, ok := metricValue(c.latest[n].state); ok {
				fmt.Fprintf(&b, "plumb_status_state{service=%s,group=%s} %g\n", quote(n), quote(groups[n]), v)
			}
		}
		b.WriteString("# HELP plumb_status_latency_seconds Response time of the last HTTP check.\n# TYPE plumb_status_latency_seconds gauge\n")
		for _, n := range names {
			if res := c.latest[n]; res.lat > 0 {
				fmt.Fprintf(&b, "plumb_status_latency_seconds{service=%s,group=%s} %g\n", quote(n), quote(groups[n]), res.lat.Seconds())
			}
		}
		c.mu.RUnlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(b.String()))
	})
}

func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}
