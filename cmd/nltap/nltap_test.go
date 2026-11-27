package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wjk22/neo-llama-top/internal/tap"
)

func TestParseConfig(t *testing.T) {
	cfg, err := parseConfig(strings.NewReader(`
# who calls llama-server
upstream = http://127.0.0.1:9000
log      = /tmp/x.jsonl
6081     = Motoko
0.0.0.0:6082 = Rico the second
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.upstream.String() != "http://127.0.0.1:9000" || cfg.logPath != "/tmp/x.jsonl" {
		t.Errorf("settings: %v %q", cfg.upstream, cfg.logPath)
	}
	want := []caller{{"127.0.0.1:6081", "Motoko"}, {"0.0.0.0:6082", "Rico the second"}}
	if len(cfg.callers) != 2 || cfg.callers[0] != want[0] || cfg.callers[1] != want[1] {
		t.Errorf("callers: %+v", cfg.callers)
	}

	// The settings have defaults; the callers do not.
	cfg, err = parseConfig(strings.NewReader("6081 = Motoko\n"))
	if err != nil || cfg.upstream.String() != "http://127.0.0.1:8080" || cfg.logPath != tap.DefaultLog {
		t.Errorf("defaults: %v %q %v", cfg.upstream, cfg.logPath, err)
	}
	for name, text := range map[string]string{
		"no caller":      "upstream = http://127.0.0.1:8080\n",
		"port twice":     "6081 = Motoko\n127.0.0.1:6081 = Rico\n",
		"no value":       "6081 =\n",
		"not a setting":  "motoko = 6081\n",
		"no equals sign": "6081 Motoko\n",
		"bad upstream":   "upstream = llama\n6081 = Motoko\n",
	} {
		if _, err := parseConfig(strings.NewReader(text)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestFindTimings(t *testing.T) {
	const obj = `{"cache_n":3926,"prompt_n":19,"prompt_ms":200.94,"predicted_n":18,"predicted_ms":297.49}`
	for name, body := range map[string]string{
		"json answer": `{"choices":[{"message":{"content":"red"}}],"usage":{"total_tokens":3963},"id":"x","timings":` + obj + `}`,
		"last chunk of a stream": "data: {\"choices\":[{\"delta\":{\"content\":\"red\"}}]}\n\n" +
			"data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}],\"timings\":" + obj + "}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"total_tokens\":3963}}\n\ndata: [DONE]\n\n",
		// Text the model wrote is inside a JSON string, so its quotes are escaped.
		"the word in the answer": `{"choices":[{"message":{"content":"say \"timings\": {\"prompt_n\": 1}"}}],"timings":` + obj + `}`,
	} {
		got := tap.Event{Timings: findTimings([]byte(body))}
		tm, ok := got.ParseTimings()
		if !ok || tm != (tap.Timings{CacheN: 3926, PromptN: 19, PromptMs: 200.94, PredictedN: 18, PredictedMs: 297.49}) {
			t.Errorf("%s: %s", name, got.Timings)
		}
	}
	for _, body := range []string{"", `{"data":[{"id":"qwen"}]}`, `{"timings":null}`, `{"timings":{"prompt_n":`, `"timings"`} {
		if got := findTimings([]byte(body)); got != nil {
			t.Errorf("findTimings(%q) = %s", body, got)
		}
	}
}

// rig is nltap between a test client and a stand-in for llama-server.
type rig struct {
	url     string // nltap's port for the caller "Motoko"
	logPath string
	lg      *logger
}

func newRig(t *testing.T, upstream http.Handler) *rig {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	u, _ := url.Parse(up.URL)
	r := &rig{logPath: filepath.Join(t.TempDir(), "log", "requests.jsonl")}
	r.lg = newLogger(r.logPath)
	front := httptest.NewServer(newTap(u, r.lg).handler("Motoko"))
	t.Cleanup(front.Close)
	r.url = front.URL
	return r
}

// events returns the log lines written so far.
func (r *rig) events(t *testing.T) []tap.Event {
	t.Helper()
	r.lg.close()
	f, err := os.Open(r.logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []tap.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e tap.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("log line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

// wait returns the log once it has n lines. nltap writes a request's line
// after the answer has gone out, so the caller can be done a moment earlier.
func (r *rig) wait(t *testing.T, n int) []tap.Event {
	t.Helper()
	var evs []tap.Event
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if evs = r.events(t); len(evs) >= n {
			break
		}
	}
	return evs
}

const answer = `{"choices":[{"message":{"content":"blue"}}],"timings":{"cache_n":0,"prompt_n":30,"prompt_ms":243.415,"predicted_n":700,"predicted_ms":11509.488}}`

// A request must reach llama-server as the caller sent it, and the answer
// must come back as llama-server wrote it.
func TestForwardsUnchanged(t *testing.T) {
	var got *http.Request
	var gotBody string
	r := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		got, gotBody = req, string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-From-Server", "yes")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, answer)
	}))

	const body = `{"model":"qwen3.6-35b-a3b","messages":[{"role":"user","content":"why is the sky blue?"}]}`
	req, _ := http.NewRequest(http.MethodPost, r.url+"/v1/chat/completions?x=1&y=two", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "open-webui")
	before := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if got.Method != http.MethodPost || got.URL.Path != "/v1/chat/completions" || got.URL.RawQuery != "x=1&y=two" || gotBody != body {
		t.Errorf("forwarded request: %s %s?%s %q", got.Method, got.URL.Path, got.URL.RawQuery, gotBody)
	}
	for k, want := range map[string]string{"Authorization": "Bearer secret", "Content-Type": "application/json", "Accept-Encoding": "identity", "User-Agent": "open-webui"} {
		if got.Header.Get(k) != want {
			t.Errorf("forwarded header %s = %q, want %q", k, got.Header.Get(k), want)
		}
	}
	if got.Host != strings.TrimPrefix(r.url, "http://") {
		t.Errorf("forwarded Host = %q", got.Host)
	}
	if got.Header.Get("X-Forwarded-For") != "" {
		t.Error("nltap added X-Forwarded-For")
	}
	if resp.StatusCode != http.StatusCreated || string(respBody) != answer || resp.Header.Get("X-From-Server") != "yes" {
		t.Errorf("answer: %d %q %v", resp.StatusCode, respBody, resp.Header)
	}

	evs := r.wait(t, 2)
	if len(evs) != 2 || evs[0].Ev != tap.Start || evs[1].Ev != tap.Done {
		t.Fatalf("log: %+v", evs)
	}
	s, d := evs[0], evs[1]
	if s.Who != "Motoko" || s.ID != d.ID || s.Path != "/v1/chat/completions" || s.Start.Before(before) || !s.End.IsZero() {
		t.Errorf("start line: %+v", s)
	}
	if d.Who != "Motoko" || d.Method != "POST" || d.Status != http.StatusCreated || d.Aborted || d.Error != "" ||
		!d.Start.Equal(s.Start) || d.End.Before(d.Start) {
		t.Errorf("done line: %+v", d)
	}
	if tm, ok := d.ParseTimings(); !ok || tm.PromptN != 30 || tm.PredictedN != 700 || tm.PromptMs != 243.415 || tm.PredictedMs != 11509.488 {
		t.Errorf("timings in the done line: %s", d.Timings)
	}
	// Nothing of the request's content is logged.
	raw, _ := os.ReadFile(r.logPath)
	for _, private := range []string{"secret", "sky", "blue", "x=1"} {
		if strings.Contains(string(raw), private) {
			t.Errorf("the log contains %q:\n%s", private, raw)
		}
	}
}

// A GET gets its one line and no start line.
func TestGetIsOneLine(t *testing.T) {
	r := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, `{"data":[{"id":"qwen3.6-35b-a3b"}]}`)
	}))
	resp, err := http.Get(r.url + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	evs := r.wait(t, 1)
	if len(evs) != 1 || evs[0].Ev != tap.Done || evs[0].Method != "GET" || evs[0].Path != "/v1/models" || evs[0].Status != 200 || evs[0].Timings != nil {
		t.Errorf("log: %+v", evs)
	}
}

// A stream must arrive chunk by chunk: the first chunk is with the caller
// while llama-server has not written the second yet.
func TestStreamsPassThrough(t *testing.T) {
	second := make(chan struct{})
	r := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hm\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-second:
		case <-time.After(5 * time.Second):
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}],\"timings\":{\"prompt_n\":20,\"prompt_ms\":198.18,\"predicted_n\":10,\"predicted_ms\":168.041}}\n\ndata: [DONE]\n\n")
	}))
	resp, err := http.Post(r.url+"/v1/chat/completions", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() { line, _ := br.ReadString('\n'); first <- line }()
	select {
	case line := <-first:
		if !strings.Contains(line, "reasoning_content") {
			t.Fatalf("first chunk: %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk was held back until the stream ended")
	}

	// While the request runs, the log already names the caller.
	evs := r.events(t)
	if len(evs) != 1 || evs[0].Ev != tap.Start || evs[0].Who != "Motoko" {
		t.Fatalf("log while the stream runs: %+v", evs)
	}

	close(second)
	rest, _ := io.ReadAll(br)
	if !strings.HasSuffix(string(rest), "data: [DONE]\n\n") {
		t.Errorf("end of the stream: %q", rest)
	}
	evs = r.wait(t, 2)
	if len(evs) != 2 || evs[1].Ev != tap.Done || evs[1].Aborted {
		t.Fatalf("log after the stream: %+v", evs)
	}
	if tm, ok := evs[1].ParseTimings(); !ok || tm.PromptN != 20 || tm.PredictedN != 10 || tm.PredictedMs != 168.041 {
		t.Errorf("timings from the last chunk: %s", evs[1].Timings)
	}
}

// A caller that hangs up mid-answer still gets its line, marked aborted.
func TestCallerHangsUp(t *testing.T) {
	released := make(chan struct{})
	r := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-req.Context().Done() // llama-server sees the cancel and stops the task
		close(released)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.url+"/v1/chat/completions", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bufio.NewReader(resp.Body).ReadString('\n')
	cancel()
	resp.Body.Close()
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("the cancel did not reach llama-server")
	}
	evs := r.wait(t, 2)
	if len(evs) != 2 || evs[1].Ev != tap.Done || !evs[1].Aborted || evs[1].Timings != nil {
		t.Errorf("log: %+v", evs)
	}
}

// When llama-server is down the caller gets a 502 and the line says why.
func TestServerDown(t *testing.T) {
	r := newRig(t, http.NotFoundHandler())
	dead := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(dead.URL)
	dead.Close()
	front := httptest.NewServer(newTap(u, r.lg).handler("Rico"))
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d, want 502", resp.StatusCode)
	}
	evs := r.wait(t, 2)
	if d := evs[len(evs)-1]; d.Ev != tap.Done || d.Who != "Rico" || d.Status != 502 || !strings.Contains(d.Error, "connection refused") || d.Aborted {
		t.Errorf("done line: %+v", d)
	}
}

// Logging must never break the traffic: with a log that cannot be written,
// requests are forwarded all the same.
func TestForwardsWhenTheLogCannotBeWritten(t *testing.T) {
	r := newRig(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, answer) }))
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o644)
	u, _ := url.Parse(r.url) // forward through the working rig, with a log under a plain file
	bad := newLogger(filepath.Join(blocker, "requests.jsonl"))
	front := httptest.NewServer(newTap(u, bad).handler("Motoko"))
	defer front.Close()

	for range 3 {
		resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(b) != answer {
			t.Errorf("answer with a broken log: %d %q", resp.StatusCode, b)
		}
	}
	bad.close()
}

// A logger whose queue is full drops lines instead of making a request wait.
func TestLogNeverBlocks(t *testing.T) {
	l := &logger{ch: make(chan tap.Event, 2)} // nobody reads this queue
	done := make(chan struct{})
	go func() {
		for range 100 {
			l.log(tap.Event{Ev: tap.Done})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("log() blocked on a full queue")
	}
}

// The log is moved aside when it gets large, and writing goes on in a new file.
func TestLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	l := newLogger(path)
	l.limit = 300
	for i := range 11 { // a line is about 66 bytes: the file is moved aside after every fifth
		l.log(tap.Event{Ev: tap.Done, ID: uint64(i + 1), Who: "Motoko", Path: "/v1/chat/completions"})
	}
	l.close()
	old, err1 := os.ReadFile(path + ".1")
	cur, err2 := os.ReadFile(path)
	if err1 != nil || err2 != nil {
		t.Fatalf("after rotation: %v, %v", err1, err2)
	}
	// One older file is kept, and the newest line is alone in the current one.
	if strings.Count(string(old), "\n") != 5 || !strings.Contains(string(old), `"id":10,`) ||
		strings.Count(string(cur), "\n") != 1 || !strings.Contains(string(cur), `"id":11,`) {
		t.Errorf("older file:\n%scurrent file:\n%s", old, cur)
	}
}
