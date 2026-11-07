// Package stats collects the numbers shown on the dashboard: tokens saved,
// estimated cost reduced and the latency the plugin adds to each request.
package stats

import (
	"math"
	"sort"
	"sync"
	"time"
)

const (
	latencyWindow = 512 // latencies kept for the p95 calculation
	recentWindow  = 25  // requests shown in the live feed
)

// Observation is what the plugin reports after processing one request.
type Observation struct {
	TokensBefore int
	TokensAfter  int
	Redactions   map[string]int
	Changed      bool
	Latency      time.Duration // time spent inside the plugin hook
}

// Recent is one row of the live feed.
type Recent struct {
	UnixMs      int64   `json:"unix_ms"`
	TokensSaved int     `json:"tokens_saved"`
	Redactions  int     `json:"redactions"`
	LatencyMs   float64 `json:"latency_ms"`
}

// Latency summarizes plugin overhead in milliseconds.
type Latency struct {
	AvgMs  float64 `json:"avg_ms"`
	P95Ms  float64 `json:"p95_ms"`
	MaxMs  float64 `json:"max_ms"`
	LastMs float64 `json:"last_ms"`
}

// Snapshot is the JSON document served at /api/stats.
type Snapshot struct {
	UptimeSeconds      float64          `json:"uptime_seconds"`
	RequestsProcessed  int64            `json:"requests_processed"`
	RequestsModified   int64            `json:"requests_modified"`
	Errors             int64            `json:"errors"`
	TokensBefore       int64            `json:"tokens_before"`
	TokensAfter        int64            `json:"tokens_after"`
	TokensSaved        int64            `json:"tokens_saved"`
	ReductionPercent   float64          `json:"reduction_percent"`
	CostPerMillionUSD  float64          `json:"cost_per_million_input_usd"`
	CostSavedUSD       float64          `json:"cost_saved_usd"`
	Redactions         map[string]int64 `json:"redactions"`
	RedactionsTotal    int64            `json:"redactions_total"`
	Latency            Latency          `json:"latency"`
	Recent             []Recent         `json:"recent"`
	TokenCountIsApprox bool             `json:"token_count_is_estimate"`
}

// Collector is safe for concurrent use.
type Collector struct {
	mu             sync.Mutex
	now            func() time.Time
	started        time.Time
	costPerMillion float64

	processed, modified, errors int64
	tokensBefore, tokensAfter   int64
	redactions                  map[string]int64

	latencyTotal time.Duration
	latencyMax   time.Duration
	latencyLast  time.Duration
	window       []float64 // ring buffer of recent latencies, in ms
	windowNext   int

	recent []Recent // oldest first, capped at recentWindow
}

// NewCollector creates a collector. costPerMillionInputUSD is the price of one
// million input tokens used to turn saved tokens into dollars.
func NewCollector(costPerMillionInputUSD float64) *Collector {
	return newCollector(costPerMillionInputUSD, time.Now)
}

func newCollector(cost float64, now func() time.Time) *Collector {
	return &Collector{
		now:            now,
		started:        now(),
		costPerMillion: cost,
		redactions:     map[string]int64{},
	}
}

// Record adds one processed request.
func (c *Collector) Record(o Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.processed++
	if o.Changed {
		c.modified++
	}
	c.tokensBefore += int64(o.TokensBefore)
	c.tokensAfter += int64(o.TokensAfter)

	redacted := 0
	for kind, n := range o.Redactions {
		c.redactions[kind] += int64(n)
		redacted += n
	}

	c.latencyTotal += o.Latency
	c.latencyLast = o.Latency
	if o.Latency > c.latencyMax {
		c.latencyMax = o.Latency
	}
	ms := durationMs(o.Latency)
	if len(c.window) < latencyWindow {
		c.window = append(c.window, ms)
	} else {
		c.window[c.windowNext] = ms
		c.windowNext = (c.windowNext + 1) % latencyWindow
	}

	c.recent = append(c.recent, Recent{
		UnixMs:      c.now().UnixMilli(),
		TokensSaved: o.TokensBefore - o.TokensAfter,
		Redactions:  redacted,
		LatencyMs:   ms,
	})
	if len(c.recent) > recentWindow {
		c.recent = append([]Recent(nil), c.recent[len(c.recent)-recentWindow:]...)
	}
}

// RecordError counts a request where the optimizer failed and the original
// request was forwarded untouched.
func (c *Collector) RecordError() {
	c.mu.Lock()
	c.errors++
	c.mu.Unlock()
}

// Snapshot returns a consistent copy of the current numbers.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	saved := c.tokensBefore - c.tokensAfter
	s := Snapshot{
		UptimeSeconds:      c.now().Sub(c.started).Seconds(),
		RequestsProcessed:  c.processed,
		RequestsModified:   c.modified,
		Errors:             c.errors,
		TokensBefore:       c.tokensBefore,
		TokensAfter:        c.tokensAfter,
		TokensSaved:        saved,
		CostPerMillionUSD:  c.costPerMillion,
		CostSavedUSD:       float64(saved) / 1_000_000 * c.costPerMillion,
		Redactions:         make(map[string]int64, len(c.redactions)),
		Recent:             make([]Recent, 0, len(c.recent)),
		TokenCountIsApprox: true,
	}
	if c.tokensBefore > 0 {
		s.ReductionPercent = float64(saved) / float64(c.tokensBefore) * 100
	}
	for k, v := range c.redactions {
		s.Redactions[k] = v
		s.RedactionsTotal += v
	}
	if c.processed > 0 {
		s.Latency.AvgMs = durationMs(c.latencyTotal) / float64(c.processed)
	}
	s.Latency.MaxMs = durationMs(c.latencyMax)
	s.Latency.LastMs = durationMs(c.latencyLast)
	s.Latency.P95Ms = percentile(c.window, 0.95)

	// Newest first for the dashboard.
	for i := len(c.recent) - 1; i >= 0; i-- {
		s.Recent = append(s.Recent, c.recent[i])
	}
	return s
}

func durationMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}
