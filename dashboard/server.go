// Package dashboard serves the single-page analytics UI and the JSON endpoint
// it polls. It uses only the standard library.
package dashboard

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/yourusername/bifrost-prompt-optimizer/stats"
)

//go:embed index.html
var indexHTML []byte

// Handler returns an http.Handler exposing:
//
//	GET /            the dashboard page
//	GET /api/stats   the current stats.Snapshot as JSON
//	GET /healthz     liveness probe
func Handler(c *stats.Collector) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_ = json.NewEncoder(w).Encode(c.Snapshot())
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(indexHTML)
	})

	return mux
}
