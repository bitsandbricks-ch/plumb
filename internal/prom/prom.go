// Package prom runs instant PromQL queries against VictoriaMetrics.
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sample is one series of an instant vector.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Client queries the Prometheus-compatible HTTP API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client with a sane timeout.
func NewClient(base string) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Query runs an instant query and returns the vector.
func (c *Client) Query(ctx context.Context, q string) ([]Sample, error) {
	u := c.BaseURL + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("query failed: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("query failed: %s", body.Error)
	}
	if body.Data.ResultType != "vector" {
		return nil, fmt.Errorf("unexpected result type %q", body.Data.ResultType)
	}
	out := make([]Sample, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		s, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("bad sample value %q", s)
		}
		out = append(out, Sample{Labels: r.Metric, Value: v})
	}
	return out, nil
}

// Scalar runs a query expected to return one series (or none, which reads as 0).
func (c *Client) Scalar(ctx context.Context, q string) (float64, error) {
	s, err := c.Query(ctx, q)
	if err != nil || len(s) == 0 {
		return 0, err
	}
	return s[0].Value, nil
}

// Point is one value of a range query.
type Point struct {
	T time.Time
	V float64
}

// Series is one series of a range query (a matrix).
type Series struct {
	Labels map[string]string
	Points []Point
}

// QueryRange runs a range query.
func (c *Client) QueryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) ([]Series, error) {
	v := url.Values{
		"query": {q},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step/time.Second), 10)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/v1/query_range?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("range query failed: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]any          `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("range query failed: %s", body.Error)
	}
	if body.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("unexpected result type %q", body.Data.ResultType)
	}
	out := make([]Series, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		s := Series{Labels: r.Metric, Points: make([]Point, 0, len(r.Values))}
		for _, p := range r.Values {
			ts, ok := p[0].(float64)
			str, ok2 := p[1].(string)
			if !ok || !ok2 {
				return nil, fmt.Errorf("bad point %v", p)
			}
			v, err := strconv.ParseFloat(str, 64)
			if err != nil {
				return nil, fmt.Errorf("bad sample value %q", str)
			}
			s.Points = append(s.Points, Point{T: time.Unix(0, int64(ts*1e9)), V: v})
		}
		out = append(out, s)
	}
	return out, nil
}
