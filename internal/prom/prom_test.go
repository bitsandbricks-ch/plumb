package prom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != "up" {
			t.Errorf("query %q", r.URL.Query().Get("query"))
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"a"},"value":[1,"0.5"]}]}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	s, err := c.Query(context.Background(), "up")
	if err != nil || len(s) != 1 || s[0].Value != 0.5 || s[0].Labels["job"] != "a" {
		t.Fatalf("s=%+v err=%v", s, err)
	}
	v, err := c.Scalar(context.Background(), "up")
	if err != nil || v != 0.5 {
		t.Fatalf("v=%v err=%v", v, err)
	}
}

func TestQueryEmptyScalarIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()
	v, err := NewClient(srv.URL).Scalar(context.Background(), "x")
	if err != nil || v != 0 {
		t.Fatalf("v=%v err=%v", v, err)
	}
}
