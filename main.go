// Command prompt-optimizer is a Bifrost plugin (built with -buildmode=plugin)
// that trims, compresses and sanitizes prompts before they reach an LLM
// provider, and serves a small analytics dashboard.
//
// Bifrost loads this file as a shared object and looks up the exported
// functions below by name: Init, GetName, PreLLMHook and Cleanup. Every other
// hook is optional and is not implemented, so the plugin never touches
// responses, streaming or the MCP gateway.
//
// All optimization logic lives in the optimizer package; this file only adapts
// Bifrost's request types to it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/yourusername/bifrost-prompt-optimizer/dashboard"
	"github.com/yourusername/bifrost-prompt-optimizer/optimizer"
	"github.com/yourusername/bifrost-prompt-optimizer/stats"
)

const (
	pluginName = "prompt-optimizer"

	defaultStatsAddr = "127.0.0.1:9091"
	defaultCostPerM  = 2.50 // USD per 1M input tokens; override in config.json

	// doneKey marks a request as already optimized. PreLLMHook runs again for
	// every fallback provider attempt, and we must not count a request twice.
	doneKey = schemas.BifrostContextKey("prompt-optimizer-done")
)

// runtimeState is replaced atomically on every Init (hot reload) so hooks never
// observe a half-built configuration.
type runtimeState struct {
	opt   *optimizer.Optimizer
	stats *stats.Collector
}

var current atomic.Pointer[runtimeState]

var dashboardServer struct {
	mu  sync.Mutex
	srv *http.Server
}

type pluginConfig struct {
	statsEnabled   bool
	statsAddr      string
	costPerMillion float64
}

// Init is called once when Bifrost loads the plugin. config is the "config"
// object from this plugin's entry in config.json.
func Init(config any) error {
	raw := map[string]any{}
	if config != nil {
		m, ok := config.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: config must be a JSON object, got %T", pluginName, config)
		}
		raw = m
	}

	optCfg, err := optimizer.ConfigFromMap(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", pluginName, err)
	}
	pCfg, err := parsePluginConfig(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", pluginName, err)
	}

	collector := stats.NewCollector(pCfg.costPerMillion)
	stopDashboard()
	current.Store(&runtimeState{opt: optimizer.New(optCfg), stats: collector})

	if pCfg.statsEnabled {
		startDashboard(pCfg.statsAddr, collector)
	}
	return nil
}

// GetName returns the plugin's unique name.
func GetName() string { return pluginName }

// PreLLMHook runs before each provider call. It rewrites the text of chat
// messages in place. It never blocks a request: on any failure the original
// request is forwarded unchanged.
func PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (out *schemas.BifrostRequest, sc *schemas.LLMPluginShortCircuit, err error) {
	out = req

	rt := current.Load()
	if rt == nil || req == nil || req.ChatRequest == nil {
		return req, nil, nil
	}
	if done, _ := ctx.Value(doneKey).(bool); done {
		return req, nil, nil // fallback attempt: already optimized
	}

	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			rt.stats.RecordError()
			ctx.Log(schemas.LogLevelError, fmt.Sprintf("%s: recovered from panic, request forwarded as-is: %v", pluginName, r))
			out, sc, err = req, nil, nil
		}
	}()

	total := optimizer.Result{Redactions: map[string]int{}}
	input := req.ChatRequest.Input
	for i := range input {
		msg := &input[i]
		if msg.Content == nil || !rt.opt.AppliesToRole(string(msg.Role)) {
			continue
		}

		if msg.Content.ContentStr != nil {
			res := rt.opt.Optimize(*msg.Content.ContentStr)
			total.Add(res)
			if res.Changed {
				text := res.Text
				msg.Content.ContentStr = &text
			}
		}

		for j := range msg.Content.ContentBlocks {
			block := &msg.Content.ContentBlocks[j]
			if block.Type != schemas.ChatContentBlockTypeText || block.Text == nil {
				continue
			}
			res := rt.opt.Optimize(*block.Text)
			total.Add(res)
			if res.Changed {
				text := res.Text
				block.Text = &text
			}
		}
	}

	rt.stats.Record(stats.Observation{
		TokensBefore: total.TokensBefore,
		TokensAfter:  total.TokensAfter,
		Redactions:   total.Redactions,
		Changed:      total.Changed,
		Latency:      time.Since(start),
	})
	ctx.SetValue(doneKey, true)

	if total.Changed {
		ctx.Log(schemas.LogLevelDebug, fmt.Sprintf("%s: saved ~%d tokens, sanitized %d items",
			pluginName, total.TokensSaved(), total.RedactionTotal()))
	}
	return req, nil, nil
}

// Cleanup is called when Bifrost shuts down.
func Cleanup() error {
	stopDashboard()
	current.Store(nil)
	return nil
}

func parsePluginConfig(m map[string]any) (pluginConfig, error) {
	cfg := pluginConfig{statsEnabled: true, statsAddr: defaultStatsAddr, costPerMillion: defaultCostPerM}

	if v, ok := m["stats_enabled"]; ok {
		b, ok := v.(bool)
		if !ok {
			return cfg, fmt.Errorf("config \"stats_enabled\" must be true or false, got %T", v)
		}
		cfg.statsEnabled = b
	}
	if v, ok := m["stats_addr"]; ok {
		s, ok := v.(string)
		if !ok || s == "" {
			return cfg, fmt.Errorf("config \"stats_addr\" must be a non-empty string like \"127.0.0.1:9091\"")
		}
		cfg.statsAddr = s
	}
	if v, ok := m["cost_per_million_input_usd"]; ok {
		switch n := v.(type) {
		case float64:
			cfg.costPerMillion = n
		case int:
			cfg.costPerMillion = float64(n)
		default:
			return cfg, fmt.Errorf("config \"cost_per_million_input_usd\" must be a number, got %T", v)
		}
		if cfg.costPerMillion < 0 {
			return cfg, fmt.Errorf("config \"cost_per_million_input_usd\" must not be negative")
		}
	}
	return cfg, nil
}

// startDashboard serves the dashboard in the background. A failure (for example
// the port is taken) is logged but never stops the gateway from starting.
func startDashboard(addr string, c *stats.Collector) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[%s] dashboard disabled: cannot listen on %s: %v", pluginName, addr, err)
		return
	}
	srv := &http.Server{
		Handler:           dashboard.Handler(c),
		ReadHeaderTimeout: 5 * time.Second,
	}

	dashboardServer.mu.Lock()
	dashboardServer.srv = srv
	dashboardServer.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[%s] dashboard stopped: %v", pluginName, err)
		}
	}()
	log.Printf("[%s] dashboard available at http://%s", pluginName, addr)
}

func stopDashboard() {
	dashboardServer.mu.Lock()
	srv := dashboardServer.srv
	dashboardServer.srv = nil
	dashboardServer.mu.Unlock()

	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
