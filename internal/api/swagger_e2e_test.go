package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/trustos/hopssh/internal/e2etest"
)

// TestE2E_APIDocs: the real server serves the API docs that make swagger
// wrote, operations and models included, with the firewall routes and no
// host. From April to 2026-11-28 the page loaded but doc.json answered 500:
// http-swagger reads swag v1's registry and swagger/ registers with swag v2.
func TestE2E_APIDocs(t *testing.T) {
	s := e2etest.Start(t)
	c := s.Anonymous()
	if got, _ := c.Do("GET", "/swagger/index.html", nil); got != http.StatusOK {
		t.Errorf("GET /swagger/index.html: HTTP %d, want 200", got)
	}
	var served map[string]any
	c.Want(http.StatusOK, "GET", "/swagger/doc.json", nil, &served)

	b, err := os.ReadFile(filepath.Join("..", "..", "swagger", "swagger.json"))
	if err != nil {
		t.Fatal(err)
	}
	var made map[string]any
	if err := json.Unmarshal(b, &made); err != nil {
		t.Fatal(err)
	}

	// With a host named, "Try it out" calls it instead of the server the
	// page came from.
	if h, ok := served["host"]; ok && h != "" {
		t.Errorf("the served API docs name the host %v; they must name none", h)
	}
	// The template writes an empty host and schemes, which swagger.json
	// leaves out.
	if served["host"] == "" {
		delete(served, "host")
	}
	if l, ok := served["schemes"].([]any); ok && len(l) == 0 {
		delete(served, "schemes")
	}
	if d := differ(served, made); len(d) > 0 {
		t.Errorf("the served API docs differ from swagger/swagger.json in %q", d)
	}
	sp, _ := served["paths"].(map[string]any)
	mp, _ := made["paths"].(map[string]any)
	if d := differ(sp, mp); len(d) > 0 {
		t.Errorf("paths that differ: %q", d)
	}
	if _, ok := sp["/api/networks/{networkID}/firewall"]; !ok {
		t.Error("the served API docs have no firewall route")
	}
}

// differ lists the keys whose values in a and b are not the same.
func differ(a, b map[string]any) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok || !reflect.DeepEqual(v, w) {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
