package discord

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
)

const (
	colorCritical = 0xE5484D
	colorWarning  = 0xF5A524
	colorInfo     = 0x3E8ED0
	colorResolved = 0x30A46C

	maxAlertsShown = 8
	footerPrefix   = "group: "
)

// Ack button IDs. Only these durations are accepted.
var ackDurations = map[string]time.Duration{
	"plumb:ack:1h":  time.Hour,
	"plumb:ack:4h":  4 * time.Hour,
	"plumb:ack:24h": 24 * time.Hour,
}

func severity(w alertmanager.Webhook) string {
	if s := w.CommonLabels["severity"]; s != "" {
		return s
	}
	for _, a := range w.Alerts {
		if a.Labels["severity"] == "critical" {
			return "critical"
		}
	}
	if len(w.Alerts) > 0 && w.Alerts[0].Labels["severity"] != "" {
		return w.Alerts[0].Labels["severity"]
	}
	return "warning"
}

func color(w alertmanager.Webhook) int {
	if w.Status == "resolved" {
		return colorResolved
	}
	switch severity(w) {
	case "critical":
		return colorCritical
	case "info":
		return colorInfo
	}
	return colorWarning
}

func alertName(w alertmanager.Webhook) string {
	for _, m := range []map[string]string{w.GroupLabels, w.CommonLabels} {
		if n := m["alertname"]; n != "" {
			return n
		}
	}
	if len(w.Alerts) > 0 {
		return w.Alerts[0].Labels["alertname"]
	}
	return "alert"
}

// AlertEmbed renders a webhook group as one Discord embed.
func AlertEmbed(w alertmanager.Webhook) *discordgo.MessageEmbed {
	firing := w.Firing()
	var title string
	if w.Status == "resolved" {
		title = "Resolved · " + alertName(w)
	} else {
		title = fmt.Sprintf("Firing · %s (%d)", alertName(w), len(firing))
	}

	shown := w.Alerts
	if w.Status != "resolved" {
		shown = firing
	}
	var b strings.Builder
	for i, a := range shown {
		if i == maxAlertsShown {
			fmt.Fprintf(&b, "…and %d more\n", len(shown)-maxAlertsShown+w.TruncatedAlerts)
			break
		}
		text := a.Annotations["summary"]
		if text == "" {
			text = a.Labels["alertname"]
		}
		fmt.Fprintf(&b, "**%s**\n", text)
		if d := a.Annotations["description"]; d != "" {
			fmt.Fprintf(&b, "%s\n", d)
		}
		if where := location(a.Labels); where != "" {
			fmt.Fprintf(&b, "`%s`\n", where)
		}
		b.WriteString("\n")
	}

	e := &discordgo.MessageEmbed{
		Title:       truncate(title, 256),
		Description: truncate(strings.TrimSpace(b.String()), 4000),
		Color:       color(w),
		Footer:      &discordgo.MessageEmbedFooter{Text: truncate(GroupFooter(w.GroupLabels), 2048)},
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Severity", Value: severity(w), Inline: true},
		},
	}
	if ns := w.CommonLabels["namespace"]; ns != "" {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "Namespace", Value: ns, Inline: true})
	}
	if since := earliest(shown); !since.IsZero() {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "Since", Value: fmt.Sprintf("<t:%d:R>", since.Unix()), Inline: true})
	}
	if len(shown) > 0 && strings.HasPrefix(shown[0].GeneratorURL, "http") {
		e.URL = shown[0].GeneratorURL
	}
	return e
}

func location(l map[string]string) string {
	var parts []string
	for _, k := range []string{"namespace", "pod", "node", "instance", "persistentvolumeclaim", "volume"} {
		if v := l[k]; v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

func earliest(as []alertmanager.Alert) time.Time {
	var t time.Time
	for _, a := range as {
		if !a.StartsAt.IsZero() && (t.IsZero() || a.StartsAt.Before(t)) {
			t = a.StartsAt
		}
	}
	return t
}

// AckButtons are attached to firing alerts.
func AckButtons() []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "Ack 1h", Style: discordgo.SecondaryButton, CustomID: "plumb:ack:1h"},
		discordgo.Button{Label: "Ack 4h", Style: discordgo.SecondaryButton, CustomID: "plumb:ack:4h"},
		discordgo.Button{Label: "Ack 24h", Style: discordgo.SecondaryButton, CustomID: "plumb:ack:24h"},
	}}}
}

// AckedButtons replaces the Ack buttons once someone acked.
func AckedButtons(who string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: truncate("Acked by "+who, 80), Style: discordgo.SuccessButton, CustomID: "plumb:acked", Disabled: true},
	}}}
}

// GroupFooter encodes the group labels so an Ack click can rebuild the
// silence matchers from the message alone (no state, survives restarts).
func GroupFooter(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Quote(labels[k]))
	}
	return footerPrefix + strings.Join(parts, " ")
}

var footerPair = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)=("(?:[^"\\]|\\.)*")`)

// ParseGroupFooter is the inverse of GroupFooter.
func ParseGroupFooter(s string) (map[string]string, error) {
	if !strings.HasPrefix(s, footerPrefix) {
		return nil, fmt.Errorf("not an alert message")
	}
	out := map[string]string{}
	for _, m := range footerPair.FindAllStringSubmatch(strings.TrimPrefix(s, footerPrefix), -1) {
		v, err := strconv.Unquote(m[2])
		if err != nil {
			return nil, err
		}
		out[m[1]] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("alert message has no group labels")
	}
	return out, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
