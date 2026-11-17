package statuspage

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Notice is a hand-written maintenance or incident note (notices.yaml).
type Notice struct {
	Title    string    `yaml:"title" json:"title"`
	Kind     string    `yaml:"kind" json:"kind"` // maintenance | incident | info
	Services []string  `yaml:"services" json:"services,omitempty"`
	Start    time.Time `yaml:"start" json:"start"`
	End      time.Time `yaml:"end" json:"end,omitempty"`
	Body     string    `yaml:"body" json:"body,omitempty"`
}

// LoadNotices reads and validates the notices file.
func LoadNotices(path string) ([]Notice, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		Notices []Notice `yaml:"notices"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	for i, n := range doc.Notices {
		switch {
		case n.Title == "":
			return nil, fmt.Errorf("notice %d: title is missing", i+1)
		case n.Kind != "maintenance" && n.Kind != "incident" && n.Kind != "info":
			return nil, fmt.Errorf("notice %q: kind must be maintenance, incident or info", n.Title)
		case n.Start.IsZero():
			return nil, fmt.Errorf("notice %q: start is missing", n.Title)
		case !n.End.IsZero() && n.End.Before(n.Start):
			return nil, fmt.Errorf("notice %q: end is before start", n.Title)
		}
	}
	return doc.Notices, nil
}

// active: shown at the top. Maintenance from 7 days before it starts.
func (n Notice) active(now time.Time) bool {
	if !n.End.IsZero() && !now.Before(n.End) {
		return false
	}
	if n.Kind == "maintenance" {
		return now.After(n.Start.AddDate(0, 0, -7))
	}
	return !now.Before(n.Start)
}

func (n Notice) inProgress(now time.Time) bool {
	return !now.Before(n.Start) && (n.End.IsZero() || now.Before(n.End))
}

func (n Notice) covers(service string) bool {
	for _, s := range n.Services {
		if s == service {
			return true
		}
	}
	return false
}

func (n Notice) overlaps(start, end time.Time) bool {
	e := n.End
	if e.IsZero() {
		e = start.Add(time.Hour * 24 * 365)
	}
	return start.Before(e) && n.Start.Before(end)
}
