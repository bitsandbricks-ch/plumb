package statuspage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bitsandbricks-ch/plumb/internal/prom"
)

// RangeQuerier is the part of prom.Client the history needs.
type RangeQuerier interface {
	Query(ctx context.Context, q string) ([]prom.Sample, error)
	QueryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) ([]prom.Series, error)
}

const (
	historyDays  = 90
	incidentDays = 14
	incidentStep = 2 * time.Minute
)

// History is what VictoriaMetrics remembers of Plumb's own checks
// (plumb_status_state, scraped from /metrics), so it survives restarts.
type History struct {
	Days      []time.Time          // local midnight of each day, oldest first
	Daily     map[string][]float64 // service -> availability per day 0..1, -1 = no data
	Incidents []Incident           // finished outages of the last 14 days, newest first
}

// Incident is a detected outage of one service.
type Incident struct {
	Service string
	Kind    State // Down or Degraded
	Start   time.Time
	End     time.Time
}

func midnight(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// availability counts "degraded" as available: slow is not down.
const availExpr = `avg by (service) (avg_over_time(clamp_max(plumb_status_state, 1)[%s:1m]))`

// LoadHistory queries daily availability for 90 days and outages for 14.
func LoadHistory(ctx context.Context, q RangeQuerier, now time.Time, loc *time.Location) (History, error) {
	today := midnight(now, loc)
	h := History{Daily: map[string][]float64{}}
	for i := historyDays - 1; i >= 0; i-- {
		h.Days = append(h.Days, today.AddDate(0, 0, -i))
	}
	index := map[string]int{}
	for i, d := range h.Days {
		index[d.Format(time.DateOnly)] = i
	}
	put := func(service string, day time.Time, v float64) {
		i, ok := index[midnight(day, loc).Format(time.DateOnly)]
		if !ok {
			return
		}
		row, ok := h.Daily[service]
		if !ok {
			row = make([]float64, historyDays)
			for j := range row {
				row[j] = -1
			}
			h.Daily[service] = row
		}
		row[i] = v
	}

	// Full days: evaluated at each day's end, looking back one day.
	firstEnd := h.Days[0].AddDate(0, 0, 1)
	lastEnd := today
	series, err := q.QueryRange(ctx, fmt.Sprintf(availExpr, "1d"), firstEnd, lastEnd, 24*time.Hour)
	if err != nil {
		return h, err
	}
	for _, s := range series {
		for _, p := range s.Points {
			put(s.Labels["service"], p.T.Add(-time.Second), p.V)
		}
	}
	// Today so far.
	secs := int(now.Sub(today).Seconds())
	if secs < 60 {
		secs = 60
	}
	samples, err := q.Query(ctx, fmt.Sprintf(availExpr, fmt.Sprintf("%ds", secs)))
	if err != nil {
		return h, err
	}
	for _, s := range samples {
		put(s.Labels["service"], now, s.Value)
	}

	// Outages: the worst state per service every 2 minutes.
	series, err = q.QueryRange(ctx, `min by (service) (plumb_status_state)`, now.AddDate(0, 0, -incidentDays), now, incidentStep)
	if err != nil {
		return h, err
	}
	for _, s := range series {
		h.Incidents = append(h.Incidents, detect(s.Labels["service"], s.Points, incidentStep, now)...)
	}
	sort.Slice(h.Incidents, func(i, j int) bool { return h.Incidents[i].Start.After(h.Incidents[j].Start) })
	return h, nil
}

// detect finds runs of down (value 0, at least 2 points) or degraded (value
// 1, at least 5 points = 10 minutes). A missing point ends a run. A run that
// is still going at the end is not "past" and is skipped.
func detect(service string, pts []prom.Point, step time.Duration, now time.Time) []Incident {
	var out []Incident
	var kind State
	var start, last time.Time
	n := 0
	flush := func(ongoing bool) {
		min := map[State]int{Down: 2, Degraded: 5}[kind]
		if kind != "" && n >= min && !ongoing {
			out = append(out, Incident{Service: service, Kind: kind, Start: start, End: last.Add(step)})
		}
		kind, n = "", 0
	}
	for i, p := range pts {
		var k State
		switch {
		case p.V < 0.5:
			k = Down
		case p.V < 1.5:
			k = Degraded
		}
		gap := i > 0 && p.T.Sub(pts[i-1].T) > step+step/2
		if k != kind || gap {
			flush(false)
			if k != "" {
				kind, start, n = k, p.T, 0
			}
		}
		if k != "" {
			n++
			last = p.T
		}
	}
	flush(len(pts) > 0 && now.Sub(pts[len(pts)-1].T) <= 2*step)
	return out
}
