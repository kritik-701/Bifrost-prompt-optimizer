package stats

import (
	"math"
	"sync"
	"testing"
	"time"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEmptySnapshot(t *testing.T) {
	s := NewCollector(2.5).Snapshot()
	if s.RequestsProcessed != 0 || s.TokensSaved != 0 || s.Latency.AvgMs != 0 || s.Latency.P95Ms != 0 {
		t.Errorf("fresh collector should be all zeros: %+v", s)
	}
	if s.ReductionPercent != 0 || s.CostSavedUSD != 0 {
		t.Errorf("no division by zero artifacts expected: %+v", s)
	}
	if s.Recent == nil || s.Redactions == nil {
		t.Error("slices and maps must be non-nil so JSON encodes [] and {}")
	}
}

func TestRecordAndSnapshot(t *testing.T) {
	c := NewCollector(2.50) // $2.50 per million input tokens

	c.Record(Observation{
		TokensBefore: 1_000_000, TokensAfter: 600_000, Changed: true,
		Redactions: map[string]int{"email": 2, "secret": 1},
		Latency:    2 * time.Millisecond,
	})
	c.Record(Observation{
		TokensBefore: 100, TokensAfter: 100, Changed: false,
		Latency: 4 * time.Millisecond,
	})

	s := c.Snapshot()
	if s.RequestsProcessed != 2 || s.RequestsModified != 1 {
		t.Errorf("request counts wrong: %+v", s)
	}
	if s.TokensBefore != 1_000_100 || s.TokensAfter != 600_100 || s.TokensSaved != 400_000 {
		t.Errorf("token totals wrong: %+v", s)
	}
	if !approx(s.CostSavedUSD, 1.0) { // 400k tokens * $2.50/M
		t.Errorf("cost saved = %v, want 1.0", s.CostSavedUSD)
	}
	if s.ReductionPercent < 39.9 || s.ReductionPercent > 40.0 {
		t.Errorf("reduction percent = %v", s.ReductionPercent)
	}
	if s.RedactionsTotal != 3 || s.Redactions["email"] != 2 {
		t.Errorf("redactions wrong: %+v", s.Redactions)
	}
	if !approx(s.Latency.AvgMs, 3) || !approx(s.Latency.MaxMs, 4) || !approx(s.Latency.LastMs, 4) {
		t.Errorf("latency wrong: %+v", s.Latency)
	}
	if len(s.Recent) != 2 || s.Recent[0].TokensSaved != 0 || s.Recent[1].TokensSaved != 400_000 {
		t.Errorf("recent feed should be newest first: %+v", s.Recent)
	}
}

func TestPercentile(t *testing.T) {
	c := NewCollector(0)
	for i := 1; i <= 100; i++ {
		c.Record(Observation{Latency: time.Duration(i) * time.Millisecond})
	}
	s := c.Snapshot()
	if !approx(s.Latency.P95Ms, 95) {
		t.Errorf("p95 = %v, want 95", s.Latency.P95Ms)
	}
	if !approx(s.Latency.MaxMs, 100) {
		t.Errorf("max = %v, want 100", s.Latency.MaxMs)
	}
}

func TestWindowsStayBounded(t *testing.T) {
	c := NewCollector(0)
	for i := 0; i < latencyWindow*3; i++ {
		c.Record(Observation{Latency: time.Millisecond})
	}
	c.mu.Lock()
	w, r := len(c.window), len(c.recent)
	c.mu.Unlock()
	if w != latencyWindow || r != recentWindow {
		t.Errorf("window=%d recent=%d, want %d and %d", w, r, latencyWindow, recentWindow)
	}
	if got := c.Snapshot().RequestsProcessed; got != int64(latencyWindow*3) {
		t.Errorf("processed = %d", got)
	}
}

func TestNegativeSavingsAreReportedHonestly(t *testing.T) {
	c := NewCollector(1)
	c.Record(Observation{TokensBefore: 3, TokensAfter: 8, Changed: true})
	if got := c.Snapshot().TokensSaved; got != -5 {
		t.Errorf("TokensSaved = %d, want -5", got)
	}
}

func TestRecordError(t *testing.T) {
	c := NewCollector(0)
	c.RecordError()
	c.RecordError()
	if got := c.Snapshot().Errors; got != 2 {
		t.Errorf("errors = %d, want 2", got)
	}
}

func TestConcurrentUse(t *testing.T) {
	c := NewCollector(1)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				c.Record(Observation{TokensBefore: 10, TokensAfter: 5, Latency: time.Microsecond,
					Redactions: map[string]int{"email": 1}})
				_ = c.Snapshot()
			}
		}()
	}
	wg.Wait()
	s := c.Snapshot()
	if s.RequestsProcessed != 4000 || s.TokensSaved != 20000 || s.Redactions["email"] != 4000 {
		t.Errorf("lost updates under concurrency: %+v", s)
	}
}
