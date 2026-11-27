package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func readSlots(t *testing.T, name string) []Slot {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var s []Slot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDecodeSlots(t *testing.T) {
	idle := readSlots(t, "slots_idle.json")[0]
	if idle.Processing || idle.NCtx != 131072 || idle.CtxTokens != 57531 || idle.Task != 22278 {
		t.Errorf("idle slot: %+v", idle)
	}
	if got := slotPhase(idle); got != phIdle {
		t.Errorf("idle slot phase = %v", got)
	}

	busy := readSlots(t, "slots_busy.json")[0]
	if !busy.Processing || busy.PromptProcessed != 32518 || busy.Decoded() != 48 || busy.Params.MaxTokens != 200 {
		t.Errorf("busy slot: %+v", busy)
	}
	// The generation prompt ends in an open think block: thinking is on, and
	// /slots does not say which part is being written.
	if got := slotPhase(busy); got != phGen {
		t.Errorf("busy slot phase = %v, want phGen", got)
	}
}

func TestGenPhase(t *testing.T) {
	for prompt, want := range map[string]phase{
		"<|im_start|>assistant\n<think>\n":               phGen,       // thinking on
		"<|im_start|>assistant\n<think>\n\n</think>\n\n": phAnswering, // thinking off
		"<|im_start|>assistant\n":                        phGen,       // unknown
		"":                                               phGen,
	} {
		if got := genPhase(prompt); got != want {
			t.Errorf("genPhase(%q) = %v, want %v", prompt, got, want)
		}
	}
}

func TestParseMetrics(t *testing.T) {
	m := parseMetrics(metricsText)
	if m["prompt_tokens_total"] != 1053211 || m["tokens_predicted_total"] != 51805 || m["requests_deferred"] != 2 {
		t.Errorf("metrics: %v", m)
	}
}

const metricsText = `# HELP llamacpp:prompt_tokens_total Number of prompt tokens processed, excluding cached tokens
# TYPE llamacpp:prompt_tokens_total counter
llamacpp:prompt_tokens_total 1053211
# HELP llamacpp:tokens_predicted_total Number of generation tokens processed
# TYPE llamacpp:tokens_predicted_total counter
llamacpp:tokens_predicted_total 51805
# HELP llamacpp:requests_deferred Number of requests deferred
# TYPE llamacpp:requests_deferred gauge
llamacpp:requests_deferred 2
`

// fakeServer answers the five endpoints from the captured test data. With
// metrics set it behaves like a server started with --metrics.
func fakeServer(t *testing.T, key string, healthStatus int, metrics bool) *httptest.Server {
	t.Helper()
	file := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if key != "" && r.Header.Get("Authorization") != "Bearer "+key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			b, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/slots", file("slots_busy.json"))
	mux.HandleFunc("/props", func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile("testdata/props.json")
		if err != nil {
			t.Error(err)
		}
		if metrics {
			b = bytes.Replace(b, []byte(`"endpoint_metrics": false`), []byte(`"endpoint_metrics": true`), 1)
		}
		w.Write(b)
	})
	mux.HandleFunc("/v1/models", file("models.json"))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if healthStatus != http.StatusOK {
			w.WriteHeader(healthStatus)
			w.Write([]byte(`{"error":{"message":"Loading model","type":"unavailable_error","code":503}}`))
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if metrics {
			w.Write([]byte(metricsText))
			return
		}
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte("{\"error\":{\"code\":501,\"message\":\"This server does not support metrics endpoint. Start it with `--metrics`\",\"type\":\"not_supported_error\"}}"))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s: nltop must only send GET", r.Method, r.URL.Path)
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient(t *testing.T) {
	srv := fakeServer(t, "secret", http.StatusOK, false)
	ctx := context.Background()
	c := newClient(srv.URL+"/", "secret")

	if st, msg := c.health(ctx); st != healthOK {
		t.Errorf("health = %v %q", st, msg)
	}
	slots, err := c.slots(ctx)
	if err != nil || len(slots) != 1 || !slots[0].Processing {
		t.Errorf("slots = %+v, %v", slots, err)
	}
	p, err := c.props(ctx)
	if err != nil || p.ModelAlias != "qwen3.6-35b-a3b" || p.TotalSlots != 1 || p.EndpointMetrics ||
		p.BuildInfo != "b1-1715275" || !p.Modalities.Vision || p.Defaults.NCtx != 131072 {
		t.Errorf("props = %+v, %v", p, err)
	}
	mm, err := c.models(ctx)
	if err != nil || mm.ID != "qwen3.6-35b-a3b" || mm.NCtxTrain != 262144 || mm.NParams != 34660610688 || mm.Size != 20531743232 {
		t.Errorf("models = %+v, %v", mm, err)
	}
	if _, err := c.metrics(ctx); err == nil || shortNetErr(err) != "HTTP 501: This server does not support metrics endpoint. Start it with `--metrics`" {
		t.Errorf("metrics error = %v", err)
	}

	if _, err := newClient(srv.URL, "wrong").slots(ctx); err == nil {
		t.Error("slots with a wrong key: no error")
	}
}

func TestHealthStates(t *testing.T) {
	ctx := context.Background()
	loading := fakeServer(t, "", http.StatusServiceUnavailable, false)
	if st, msg := newClient(loading.URL, "").health(ctx); st != healthLoading || msg != "loading model" {
		t.Errorf("loading server: %v %q", st, msg)
	}
	gone := fakeServer(t, "", http.StatusOK, false)
	url := gone.URL
	gone.Close()
	if st, msg := newClient(url, "").health(ctx); st != healthDown || msg != "connection refused" {
		t.Errorf("stopped server: %v %q", st, msg)
	}
}

// poll must deliver slots every round and the slower endpoints with them at start.
func TestPoll(t *testing.T) {
	srv := fakeServer(t, "", http.StatusOK, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan pollMsg, 8)
	go poll(ctx, newClient(srv.URL, ""), 100*time.Millisecond, func(m pollMsg) { got <- m })

	select {
	case m := <-got:
		if m.slotsErr != nil || len(m.slots) != 1 {
			t.Errorf("first round slots: %+v %v", m.slots, m.slotsErr)
		}
		if !m.hasHealth || m.health != healthOK || !m.hasProps || m.props == nil || m.meta == nil {
			t.Errorf("first round: health %v/%v props %v meta %v", m.hasHealth, m.health, m.props, m.meta)
		}
		if m.hasMetrics {
			t.Error("metrics read although /props says the endpoint is off")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no poll result")
	}
	select {
	case m := <-got:
		if m.hasProps {
			t.Error("props read again in the second round")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no second poll result")
	}
}

// With --metrics the server has a queue length and totals since its start;
// they go into the server panel.
func TestPollWithMetrics(t *testing.T) {
	srv := fakeServer(t, "", http.StatusOK, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan pollMsg, 8)
	go poll(ctx, newClient(srv.URL, ""), 100*time.Millisecond, func(m pollMsg) { got <- m })

	select {
	case p := <-got:
		if !p.hasMetrics || p.metrics["requests_deferred"] != 2 {
			t.Fatalf("first round metrics: %v %v", p.hasMetrics, p.metrics)
		}
		m := newModel(config{url: srv.URL, interval: 500 * time.Millisecond, version: "test"})
		m.Update(p)
		out := text(screen(m, 160, 40))
		for _, want := range []string{"queued 2", "since start 1.05M prompt · 51.8k gen"} {
			if !strings.Contains(out, want) {
				t.Errorf("server panel lacks %q", want)
			}
		}
		if strings.Contains(out, "/metrics off") {
			t.Error("server panel says /metrics is off")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no poll result")
	}
}
