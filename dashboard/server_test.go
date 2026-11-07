package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kritik-701/bifrost-prompt-optimizer/stats"
)

func TestStatsEndpointReturnsJSON(t *testing.T) {
	c := stats.NewCollector(2.5)
	c.Record(stats.Observation{TokensBefore: 100, TokensAfter: 60, Changed: true, Latency: time.Millisecond})

	srv := httptest.NewServer(Handler(c))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var snap stats.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.TokensSaved != 40 || snap.RequestsProcessed != 1 {
		t.Errorf("unexpected snapshot: %+v", snap)
	}
}

func TestDashboardPageIsServed(t *testing.T) {
	srv := httptest.NewServer(Handler(stats.NewCollector(1)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Tokens Saved") {
		t.Errorf("dashboard page not served correctly (status %d)", resp.StatusCode)
	}
}

func TestUnknownPathAndMethods(t *testing.T) {
	h := Handler(stats.NewCollector(1))

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/nope", http.StatusNotFound},
		{http.MethodPost, "/api/stats", http.StatusMethodNotAllowed},
		{http.MethodGet, "/healthz", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}
