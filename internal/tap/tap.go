// Package tap is the log that nltap writes and nltop reads: one JSON object
// per line.
package tap

import (
	"encoding/json"
	"time"
)

// DefaultLog is where nltap writes when its config names no other file, and
// where nltop looks.
const DefaultLog = "/var/log/nltap/requests.jsonl"

// Kinds of lines.
const (
	Up    = "up"    // nltap started; requests it had in flight before are gone
	Start = "start" // a POST arrived, so the caller is known while it runs
	Done  = "done"  // a request finished; this is the one line every request gets
)

// Event is one line of the log.
type Event struct {
	Ev      string          `json:"ev"`
	ID      uint64          `json:"id,omitempty"` // counts up from 1 after each "up"
	Who     string          `json:"who,omitempty"`
	Start   time.Time       `json:"start,omitzero"`
	End     time.Time       `json:"end,omitzero"`
	Method  string          `json:"method,omitempty"`
	Path    string          `json:"path,omitempty"`
	Status  int             `json:"status,omitempty"`  // HTTP status llama-server answered with
	Aborted bool            `json:"aborted,omitempty"` // the answer did not reach its end
	Error   string          `json:"error,omitempty"`   // llama-server could not be reached
	Timings json.RawMessage `json:"timings,omitempty"` // the "timings" object as llama-server sent it
}

// Timings is the part of llama-server's "timings" object that nltop uses to
// find the same request in the server's journal.
type Timings struct {
	CacheN      int     `json:"cache_n"`
	PromptN     int     `json:"prompt_n"`
	PromptMs    float64 `json:"prompt_ms"`
	PredictedN  int     `json:"predicted_n"`
	PredictedMs float64 `json:"predicted_ms"`
}

// ParseTimings reads the event's timings; ok is false when it has none.
func (e Event) ParseTimings() (t Timings, ok bool) {
	if len(e.Timings) == 0 {
		return t, false
	}
	return t, json.Unmarshal(e.Timings, &t) == nil
}
