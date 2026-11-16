// Package alertmanager has the webhook payload types and a small API client.
//
// The only write Plumb makes anywhere is CreateSilence (the Ack button).
package alertmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Webhook is the payload Alertmanager sends to webhook_configs receivers.
type Webhook struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

// Alert is one alert inside a webhook payload.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Firing returns the alerts that are still firing.
func (w Webhook) Firing() []Alert {
	var out []Alert
	for _, a := range w.Alerts {
		if a.Status == "firing" {
			out = append(out, a)
		}
	}
	return out
}

// Matcher is an Alertmanager label matcher.
type Matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// Silence is a silence to create.
type Silence struct {
	Matchers  []Matcher `json:"matchers"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedBy string    `json:"createdBy"`
	Comment   string    `json:"comment"`
}

// ActiveAlert is an alert as returned by GET /api/v2/alerts.
type ActiveAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	Status      struct {
		State string `json:"state"`
	} `json:"status"`
}

// Client talks to the Alertmanager v2 API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client with a sane timeout.
func NewClient(base string) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// CreateSilence creates a silence and returns its ID.
func (c *Client) CreateSilence(ctx context.Context, s Silence) (string, error) {
	if len(s.Matchers) == 0 {
		return "", fmt.Errorf("refusing to create a silence without matchers")
	}
	body, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/v2/silences", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		SilenceID string `json:"silenceID"`
	}
	if err := c.do(req, &out); err != nil {
		return "", err
	}
	return out.SilenceID, nil
}

// ActiveAlerts lists alerts that are firing and neither silenced nor inhibited.
func (c *Client) ActiveAlerts(ctx context.Context) ([]ActiveAlert, error) {
	q := url.Values{"active": {"true"}, "silenced": {"false"}, "inhibited": {"false"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v2/alerts?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var out []ActiveAlert
	return out, c.do(req, &out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("alertmanager %s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
