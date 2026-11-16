package statuspage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Labels and annotations an app uses to appear on the status page.
const (
	LabelShow     = "status.reyki.me/show" // label, "true" to opt in
	AnnName       = "status.reyki.me/name"
	AnnURL        = "status.reyki.me/url"   // what to probe; default https://<first Host>/
	AnnGroup      = "status.reyki.me/group" // default "Apps"
	AnnOrder      = "status.reyki.me/order"
	annReykiName  = "reyki.me/name" // reused from api.reyki.me's listing
	annReykiOrder = "reyki.me/order"
)

// Discoverer lists opted-in Traefik IngressRoutes. It needs only
// get/list on ingressroutes.traefik.io.
type Discoverer struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// InClusterDiscoverer uses the pod's service account.
func InClusterDiscoverer() (*Discoverer, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return nil, fmt.Errorf("not running in a cluster")
	}
	const dir = "/var/run/secrets/kubernetes.io/serviceaccount/"
	token, err := os.ReadFile(dir + "token")
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(dir + "ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	return &Discoverer{
		BaseURL: "https://" + host + ":" + port,
		Token:   strings.TrimSpace(string(token)),
		HTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}},
	}, nil
}

var hostRe = regexp.MustCompile("Host\\(`([^`]+)`\\)")

// Discover returns one Target per opted-in IngressRoute.
func (d *Discoverer) Discover(ctx context.Context) ([]Target, error) {
	u := d.BaseURL + "/apis/traefik.io/v1alpha1/ingressroutes?" + url.Values{"labelSelector": {LabelShow + "=true"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("listing IngressRoutes: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Routes []struct {
					Match string `json:"match"`
				} `json:"routes"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	var out []Target
	for _, it := range list.Items {
		a := it.Metadata.Annotations
		t := Target{Name: first(a[AnnName], a[annReykiName], it.Metadata.Name), Group: first(a[AnnGroup], "Apps"), URL: a[AnnURL]}
		t.Order, _ = strconv.Atoi(first(a[AnnOrder], a[annReykiOrder], "100"))
		if t.URL == "" {
			for _, r := range it.Spec.Routes {
				if m := hostRe.FindStringSubmatch(r.Match); m != nil {
					t.URL = "https://" + m[1] + "/"
					break
				}
			}
		}
		if t.URL == "" || !(strings.HasPrefix(t.URL, "https://") || strings.HasPrefix(t.URL, "http://")) {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
