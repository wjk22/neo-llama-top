package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Slot is one entry of GET /slots. The server reports only counters and
// sampler settings here; prompt and generated text are not included.
type Slot struct {
	ID              int  `json:"id"`
	NCtx            int  `json:"n_ctx"`
	Processing      bool `json:"is_processing"`
	Task            int  `json:"id_task"`
	CtxTokens       int  `json:"n_prompt_tokens"`           // tokens held in the slot's context
	PromptProcessed int  `json:"n_prompt_tokens_processed"` // prompt tokens evaluated for this task
	PromptCached    int  `json:"n_prompt_tokens_cache"`     // prompt tokens reused from the cache
	Params          struct {
		Temperature      float64 `json:"temperature"`
		TopK             int     `json:"top_k"`
		TopP             float64 `json:"top_p"`
		MinP             float64 `json:"min_p"`
		MaxTokens        int     `json:"max_tokens"`
		ChatFormat       string  `json:"chat_format"`
		ReasoningFormat  string  `json:"reasoning_format"`
		GenerationPrompt string  `json:"generation_prompt"`
	} `json:"params"`
	NextToken []struct {
		NRemain  int `json:"n_remain"`
		NDecoded int `json:"n_decoded"`
	} `json:"next_token"`
}

// Decoded returns the number of tokens generated so far for the current task.
func (s Slot) Decoded() int {
	if len(s.NextToken) == 0 {
		return 0
	}
	return s.NextToken[0].NDecoded
}

// Props is the part of GET /props that nltop shows.
type Props struct {
	TotalSlots int    `json:"total_slots"`
	ModelAlias string `json:"model_alias"`
	ModelFtype string `json:"model_ftype"`
	ModelPath  string `json:"model_path"`
	Modalities struct {
		Vision bool `json:"vision"`
		Video  bool `json:"video"`
		Audio  bool `json:"audio"`
	} `json:"modalities"`
	EndpointMetrics bool   `json:"endpoint_metrics"`
	BuildInfo       string `json:"build_info"`
	Sleeping        bool   `json:"is_sleeping"`
	Defaults        struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// ModelMeta is the "meta" block of the first model in GET /v1/models.
type ModelMeta struct {
	ID        string
	NCtx      int   `json:"n_ctx"`
	NCtxTrain int   `json:"n_ctx_train"`
	NParams   int64 `json:"n_params"`
	Size      int64 `json:"size"`
	NVocab    int   `json:"n_vocab"`
}

type healthState int

const (
	healthUnknown healthState = iota
	healthOK
	healthLoading
	healthDown
)

type client struct {
	base string
	key  string
	http *http.Client
}

func newClient(base, key string) *client {
	return &client{
		base: strings.TrimRight(base, "/"),
		key:  key,
		http: &http.Client{Timeout: 2 * time.Second},
	}
}

// httpError is a non-200 answer from the server.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string {
	if e.msg != "" {
		return fmt.Sprintf("HTTP %d: %s", e.status, e.msg)
	}
	return fmt.Sprintf("HTTP %d", e.status)
}

func (c *client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, &httpError{status: resp.StatusCode, msg: e.Error.Message}
	}
	return body, nil
}

func (c *client) slots(ctx context.Context) ([]Slot, error) {
	body, err := c.get(ctx, "/slots")
	if err != nil {
		return nil, err
	}
	var s []Slot
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("/slots: %w", err)
	}
	return s, nil
}

func (c *client) props(ctx context.Context) (*Props, error) {
	body, err := c.get(ctx, "/props")
	if err != nil {
		return nil, err
	}
	var p Props
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("/props: %w", err)
	}
	return &p, nil
}

func (c *client) models(ctx context.Context) (*ModelMeta, error) {
	body, err := c.get(ctx, "/v1/models")
	if err != nil {
		return nil, err
	}
	var r struct {
		Data []struct {
			ID   string    `json:"id"`
			Meta ModelMeta `json:"meta"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("/v1/models: %w", err)
	}
	if len(r.Data) == 0 {
		return nil, errors.New("/v1/models: no model listed")
	}
	m := r.Data[0].Meta
	m.ID = r.Data[0].ID
	return &m, nil
}

// health reports the server state and, when it is not ok, a short reason.
func (c *client) health(ctx context.Context) (healthState, string) {
	_, err := c.get(ctx, "/health")
	if err == nil {
		return healthOK, ""
	}
	var he *httpError
	if errors.As(err, &he) {
		if he.status == http.StatusServiceUnavailable {
			return healthLoading, "loading model"
		}
		return healthDown, he.Error()
	}
	return healthDown, shortNetErr(err)
}

// metrics reads GET /metrics, which exists only when the server runs with --metrics.
func (c *client) metrics(ctx context.Context) (map[string]float64, error) {
	body, err := c.get(ctx, "/metrics")
	if err != nil {
		return nil, err
	}
	return parseMetrics(string(body)), nil
}

// parseMetrics reads the Prometheus text format llama-server writes:
// "llamacpp:<name> <value>" lines, with # comments in between.
func parseMetrics(text string) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		name, val, ok := strings.Cut(line, " ")
		if !ok || strings.Contains(name, "{") {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			continue
		}
		out[strings.TrimPrefix(name, "llamacpp:")] = f
	}
	return out
}

func shortNetErr(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	var he *httpError
	if errors.As(err, &he) {
		return he.Error()
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

// pollMsg is one round of GETs. Slots are read every round; the slower-moving
// endpoints are read every few rounds and carry a "has" flag.
type pollMsg struct {
	at       time.Time
	slots    []Slot
	slotsErr error
	latency  time.Duration

	hasHealth bool
	health    healthState
	healthMsg string

	hasProps bool
	props    *Props
	meta     *ModelMeta

	hasMetrics bool
	metrics    map[string]float64
}

// poll reads the server once per interval until ctx ends and hands each round
// to send. All requests are GETs.
func poll(ctx context.Context, c *client, interval time.Duration, send func(pollMsg)) {
	every := func(d time.Duration) int { return max(1, int(d/interval)) }
	healthEvery, propsEvery, metricsEvery := every(2*time.Second), every(30*time.Second), every(2*time.Second)

	tick := time.NewTicker(interval)
	defer tick.Stop()
	wasOK := false
	metricsOn := false
	for n := 0; ; n++ {
		var m pollMsg
		m.at = time.Now()
		m.slots, m.slotsErr = c.slots(ctx)
		m.latency = time.Since(m.at)

		if n%healthEvery == 0 || m.slotsErr != nil || !wasOK {
			m.hasHealth = true
			m.health, m.healthMsg = c.health(ctx)
		}
		recovered := m.hasHealth && m.health == healthOK && !wasOK
		if m.hasHealth {
			wasOK = m.health == healthOK
		}
		if wasOK && (n%propsEvery == 0 || recovered) {
			if p, err := c.props(ctx); err == nil {
				m.hasProps = true
				m.props = p
				metricsOn = p.EndpointMetrics
				m.meta, _ = c.models(ctx)
			}
		}
		if wasOK && metricsOn && n%metricsEvery == 0 {
			if mm, err := c.metrics(ctx); err == nil {
				m.hasMetrics = true
				m.metrics = mm
			}
		}
		if ctx.Err() != nil {
			return
		}
		send(m)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
