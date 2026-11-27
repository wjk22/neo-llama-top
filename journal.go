package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Request is one completion as llama-server logs it: a slot is launched, the
// timings are printed, the slot is released.
type Request struct {
	Slot int
	Task int
	Who  string // caller; the journal does not name one, so this stays empty for now

	Start time.Time
	End   time.Time
	Done  bool

	PromptTokens int // prompt tokens evaluated (cached ones not counted)
	PromptMs     float64
	PromptTPS    float64
	GenTokens    int
	GenMs        float64
	GenTPS       float64

	CtxTokens int // tokens in the slot's context when it was released
	Truncated bool
	Cancelled bool

	// Progress the server logs every few seconds while a long prompt is evaluated.
	PromptSeen     int
	PromptProgress float64
}

// Cached estimates how many prompt tokens came from the slot's cache: what was
// in the context at release, minus what this request evaluated and generated.
// The last generated token is not kept in the context, hence the +1.
func (r Request) Cached() int {
	if !r.Done || r.CtxTokens == 0 || r.Truncated || r.Cancelled {
		return 0 // a cancelled request logs no token counts to subtract
	}
	return max(0, r.CtxTokens-r.PromptTokens-r.GenTokens+1)
}

// PromptTotal estimates how many prompt tokens this request has to evaluate,
// from the progress the server logs. The logged fraction counts the cached
// tokens too, so they are passed in. Zero when no progress line was seen.
func (r Request) PromptTotal(cached int) int {
	if r.PromptProgress <= 0 {
		return 0
	}
	return int(float64(cached+r.PromptSeen)/r.PromptProgress+0.5) - cached
}

// journalEvent is one change the journal reports.
type journalEvent struct {
	req       *Request  // new state of a request, or nil
	procStart time.Time // start of the llama-server process, when it became known
	reset     bool      // a new server process logs now; requests in flight are gone
}

type journalMsg struct {
	events []journalEvent
	note   string // why the journal gives nothing (missing tool, no permission, ...)
}

var (
	// llama-server prefixes each line with the time since its start,
	// minutes.seconds.milliseconds.microseconds, and a level letter.
	reLogLine  = regexp.MustCompile(`^(\d+)\.(\d{2})\.(\d{3})\.(\d{3}) [A-Z] (.*)$`)
	reSlotLine = regexp.MustCompile(`^slot\s+(\w+): id\s+(\d+) \| task\s+(-?\d+) \| (.*)$`)
	reEval     = regexp.MustCompile(`^(prompt )?eval time =\s*([\d.]+) ms /\s*(\d+) tokens \(.*,\s*([\d.]+) tokens per second\)`)
	reProgress = regexp.MustCompile(`^prompt processing, n_tokens =\s*(\d+), progress = ([\d.]+)`)
	reRelease  = regexp.MustCompile(`^stop processing: n_tokens = (\d+), truncated = (\d+)`)
	reCancel   = regexp.MustCompile(`^srv\s+stop: cancel task, id_task = (\d+)`)
)

// journalParser turns llama-server log lines into request events.
type journalParser struct {
	live      map[int]*Request // by slot
	pid       string
	procStart time.Time
}

func newJournalParser() *journalParser {
	return &journalParser{live: map[int]*Request{}}
}

// parse takes one journal entry. ok is false when the line changes nothing.
func (p *journalParser) parse(ts time.Time, pid, msg string) (ev journalEvent, ok bool) {
	if pid != "" && pid != p.pid {
		if p.pid != "" {
			ev.reset, ok = true, true
			p.live = map[int]*Request{}
			p.procStart = time.Time{}
		}
		p.pid = pid
	}
	m := reLogLine.FindStringSubmatch(msg)
	if m == nil {
		return ev, ok
	}
	mins, _ := strconv.Atoi(m[1])
	sec, _ := strconv.Atoi(m[2])
	ms, _ := strconv.Atoi(m[3])
	us, _ := strconv.Atoi(m[4])
	since := time.Duration(mins)*time.Minute + time.Duration(sec)*time.Second +
		time.Duration(ms)*time.Millisecond + time.Duration(us)*time.Microsecond
	if start := ts.Add(-since); p.procStart.IsZero() || absDur(start.Sub(p.procStart)) > 2*time.Second {
		p.procStart = start
		ev.procStart, ok = start, true
	}
	body := m[5]

	if c := reCancel.FindStringSubmatch(body); c != nil {
		task, _ := strconv.Atoi(c[1])
		for _, r := range p.live {
			if r.Task == task {
				r.Cancelled = true
			}
		}
		return ev, ok
	}
	s := reSlotLine.FindStringSubmatch(body)
	if s == nil {
		return ev, ok
	}
	fn := s[1]
	slot, _ := strconv.Atoi(s[2])
	task, _ := strconv.Atoi(s[3])
	rest := strings.TrimSpace(s[4])
	if task < 0 {
		return ev, ok // slot selection, before the task has its number
	}

	r := p.live[slot]
	if r == nil || r.Task != task || fn == "launch_slot_" {
		// A launch line, or the first line of a request whose launch was
		// logged before the part of the journal we read.
		r = &Request{Slot: slot, Task: task, Start: ts}
		p.live[slot] = r
		if fn == "launch_slot_" {
			return emit(ev, r), true
		}
	}

	switch fn {
	case "print_timing":
		if e := reEval.FindStringSubmatch(rest); e != nil {
			msv, _ := strconv.ParseFloat(e[2], 64)
			n, _ := strconv.Atoi(e[3])
			tps, _ := strconv.ParseFloat(e[4], 64)
			if e[1] != "" {
				r.PromptMs, r.PromptTokens, r.PromptTPS = msv, n, tps
			} else {
				r.GenMs, r.GenTokens, r.GenTPS = msv, n, tps
			}
			return ev, ok // the release line follows at once and reports the request
		}
		if g := reProgress.FindStringSubmatch(rest); g != nil {
			r.PromptSeen, _ = strconv.Atoi(g[1])
			r.PromptProgress, _ = strconv.ParseFloat(g[2], 64)
			return emit(ev, r), true
		}
	case "release":
		if g := reRelease.FindStringSubmatch(rest); g != nil {
			r.CtxTokens, _ = strconv.Atoi(g[1])
			r.Truncated = g[2] != "0"
			r.End = ts
			r.Done = true
			delete(p.live, slot)
			return emit(ev, r), true
		}
	}
	return ev, ok
}

func emit(ev journalEvent, r *Request) journalEvent {
	c := *r
	ev.req = &c
	return ev
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// journalEntry is the part of journalctl's JSON output that is read.
type journalEntry struct {
	TS      string          `json:"__REALTIME_TIMESTAMP"`
	PID     string          `json:"_PID"`
	Message json.RawMessage `json:"MESSAGE"`
}

// text returns MESSAGE, which journalctl writes as a string, or as an array of
// byte values when the line is not valid UTF-8.
func (e journalEntry) text() string {
	var s string
	if json.Unmarshal(e.Message, &s) == nil {
		return s
	}
	var b []byte
	var ints []int
	if json.Unmarshal(e.Message, &ints) == nil {
		for _, v := range ints {
			b = append(b, byte(v))
		}
	}
	return string(b)
}

func (e journalEntry) time() time.Time {
	us, err := strconv.ParseInt(e.TS, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMicro(us)
}

// followJournal tails the unit's journal until ctx ends: first everything
// since yesterday 00:00, so the last request and today's totals are known
// right away, then new lines as they are written.
func followJournal(ctx context.Context, unit string, send func(journalMsg)) {
	// Without permission journalctl gives no error for a system unit: it shows
	// the user's own journal, which has nothing. So the permission is checked
	// here, and its explanation replaces whatever journalctl has to say.
	if denied := journalDenied(journalDirs, currentUser()); denied != "" {
		send(journalMsg{note: denied})
		plain := send
		send = func(m journalMsg) {
			if m.note != "" {
				m.note = denied
			}
			plain(m)
		}
	}
	p := newJournalParser()
	var last time.Time // newest entry handled, so a restarted journalctl does not repeat the backlog
	for {
		err := runJournal(ctx, unit, p, &last, send)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, exec.ErrNotFound) {
			send(journalMsg{note: "journalctl not found: request history needs systemd's journal"})
			return
		}
		if err != nil {
			send(journalMsg{note: "journalctl: " + err.Error()})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// journalDirs is where journald keeps the system journal: on disk, or in
// memory when nothing is stored.
var journalDirs = []string{"/var/log/journal", "/run/log/journal"}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "this user"
}

// journalDenied checks whether the system journal exists and cannot be opened
// for lack of permission. If so it returns what to tell the user, else "".
func journalDenied(dirs []string, who string) string {
	denied := ""
	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*", "system.journal"))
		if _, err := os.ReadDir(dir); errors.Is(err, fs.ErrPermission) {
			denied = dir
		}
		for _, f := range files {
			h, err := os.Open(f)
			if err == nil {
				h.Close()
				return ""
			}
			if errors.Is(err, fs.ErrPermission) {
				denied = dir
			}
		}
	}
	if denied == "" {
		return ""
	}
	return fmt.Sprintf("The journal can't be read: %s has no permission for the system journal in %s. "+
		"Its readers are root and the members of the systemd-journal group (on Debian adm too): "+
		"run \"sudo usermod -aG systemd-journal %s\" and log in again, or start nltop as root.", who, denied, who)
}

func runJournal(ctx context.Context, unit string, p *journalParser, last *time.Time, send func(journalMsg)) error {
	cmd := exec.CommandContext(ctx, "journalctl",
		"--unit", unit, "--output", "json", "--output-fields", "MESSAGE,_PID",
		"--since", "yesterday", "--follow", "--no-tail", "--no-pager")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	// journalctl explains a missing permission on stderr and keeps running.
	var mu sync.Mutex
	var firstErr string
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			mu.Lock()
			first := firstErr == ""
			if first {
				firstErr = line
			}
			mu.Unlock()
			if first {
				send(journalMsg{note: strings.TrimPrefix(line, "Hint: ")})
			}
		}
	}()

	// Entries up to resume were handled before journalctl was restarted. The
	// server logs several lines within one microsecond, so equal timestamps
	// are normal inside a run and only compared against the earlier run.
	resume := *last
	events := make(chan journalEvent, 1024)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 4<<20)
		for sc.Scan() {
			var e journalEntry
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			ts := e.time()
			if ts.IsZero() || !resume.IsZero() && !ts.After(resume) {
				continue
			}
			if ts.After(*last) {
				*last = ts
			}
			if ev, ok := p.parse(ts, e.PID, e.text()); ok {
				events <- ev
			}
		}
	}()

	// Hand events over in batches: the backlog at start is thousands of lines.
	for ev := range events {
		batch := []journalEvent{ev}
	drain:
		for len(batch) < 1024 {
			select {
			case e, ok := <-events:
				if !ok {
					break drain
				}
				batch = append(batch, e)
			default:
				break drain
			}
		}
		send(journalMsg{events: batch})
	}

	err = cmd.Wait()
	mu.Lock()
	defer mu.Unlock()
	if firstErr != "" {
		return errors.New(firstErr)
	}
	return err
}
