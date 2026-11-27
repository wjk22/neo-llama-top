package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/wjk22/neo-llama-top/internal/tap"
)

// tapper forwards requests to llama-server and logs each one.
type tapper struct {
	proxy *httputil.ReverseProxy
	log   *logger
	next  atomic.Uint64 // request counter
}

type captureKey struct{}

// capture collects what the log line needs while a response passes through.
type capture struct {
	status int
	tail   []byte // the end of the response body
	eof    bool   // the body was read to its end
	err    string // llama-server could not be reached
}

const tailKeep = 32 << 10 // bytes of the response end that are kept

func (c *capture) add(p []byte) {
	c.tail = append(c.tail, p...)
	if len(c.tail) > 2*tailKeep {
		c.tail = append(c.tail[:0], c.tail[len(c.tail)-tailKeep:]...)
	}
}

// tailBody passes a response body through and remembers its end.
type tailBody struct {
	io.ReadCloser
	c *capture
}

func (b *tailBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.c.add(p[:n])
	if err == io.EOF {
		b.c.eof = true
	}
	return n, err
}

func newTap(upstream *url.URL, lg *logger) *tapper {
	t := &tapper{log: lg}
	t.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Host = r.In.Host // the Host header as the caller sent it
		},
		// Go must not ask for gzip on its own and unpack it: the answer goes
		// back exactly as llama-server wrote it.
		Transport: &http.Transport{
			DisableCompression:  true,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		},
		FlushInterval: -1, // pass every chunk on at once: streams stay streams
		ModifyResponse: func(resp *http.Response) error {
			if c, ok := resp.Request.Context().Value(captureKey{}).(*capture); ok {
				c.status = resp.StatusCode
				resp.Body = &tailBody{ReadCloser: resp.Body, c: c}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if c, ok := r.Context().Value(captureKey{}).(*capture); ok {
				c.err = err.Error()
			}
			w.WriteHeader(http.StatusBadGateway)
		},
		ErrorLog: log.New(io.Discard, "", 0), // failures go into the request's log line
	}
	return t
}

// handler serves one caller's port.
func (t *tapper) handler(who string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ev := tap.Event{ID: t.next.Add(1), Who: who, Start: time.Now(), Method: r.Method, Path: r.URL.Path}
		if r.Method == http.MethodPost {
			// Only a POST can keep a slot busy; its start is logged so the
			// caller is known while it runs.
			start := ev
			start.Ev = tap.Start
			t.log.log(start)
		}
		c := &capture{}
		// Deferred, because the proxy ends a request whose caller hung up by
		// panicking with http.ErrAbortHandler.
		defer func() {
			ev.Ev, ev.End = tap.Done, time.Now()
			ev.Status, ev.Error = c.status, c.err
			if c.err != "" {
				ev.Status = http.StatusBadGateway
			} else {
				ev.Aborted = !c.eof
				ev.Timings = findTimings(c.tail)
			}
			t.log.log(ev)
		}()
		t.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), captureKey{}, c)))
	})
}

// findTimings returns the last "timings" object in the end of a response: the
// one in a JSON answer, or in the final chunk of a stream. Nil if there is none.
func findTimings(tail []byte) json.RawMessage {
	key := []byte(`"timings"`)
	i := bytes.LastIndex(tail, key)
	if i < 0 {
		return nil
	}
	rest := bytes.TrimLeft(tail[i+len(key):], " \t")
	if len(rest) == 0 || rest[0] != ':' {
		return nil
	}
	var obj json.RawMessage
	if json.NewDecoder(bytes.NewReader(rest[1:])).Decode(&obj) != nil || len(obj) == 0 || obj[0] != '{' {
		return nil
	}
	return obj
}

// logger appends events to the log file from its own goroutine. Handing it an
// event never blocks and never fails: when the file cannot be written, or the
// queue is full, the line is lost and the traffic goes on.
type logger struct {
	path  string
	ch    chan tap.Event
	sync  chan chan struct{}
	limit int64 // size at which the file is moved aside
}

const maxLogSize = 16 << 20

func newLogger(path string) *logger {
	l := &logger{path: path, ch: make(chan tap.Event, 4096), sync: make(chan chan struct{}), limit: maxLogSize}
	go l.run()
	return l
}

func (l *logger) log(e tap.Event) {
	select {
	case l.ch <- e:
	default:
	}
}

// close writes out what is queued and closes the file. The logger stays
// usable; later events open the file again.
func (l *logger) close() {
	done := make(chan struct{})
	l.sync <- done
	<-done
}

func (l *logger) run() {
	var f *os.File
	var size int64
	var lastComplaint time.Time
	complain := func(err error) {
		if time.Since(lastComplaint) > time.Minute {
			lastComplaint = time.Now()
			fmt.Fprintln(os.Stderr, "nltap: cannot write the log, requests are still forwarded:", err)
		}
	}
	write := func(e tap.Event) {
		line, err := json.Marshal(e)
		if err != nil {
			return
		}
		if f == nil {
			_ = os.MkdirAll(filepath.Dir(l.path), 0o755)
			if f, err = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err != nil {
				f = nil
				complain(err)
				return
			}
			size = 0
			if st, err := f.Stat(); err == nil {
				size = st.Size()
			}
		}
		n, err := f.Write(append(line, '\n'))
		size += int64(n)
		if err != nil {
			complain(err)
			f.Close()
			f = nil
			return
		}
		if size > l.limit {
			// Keep one older file; nltop only needs the last two days.
			f.Close()
			f = nil
			if err := os.Rename(l.path, l.path+".1"); err != nil {
				complain(err)
			}
		}
	}
	for {
		select {
		case e := <-l.ch:
			write(e)
		case done := <-l.sync:
			for len(l.ch) > 0 {
				write(<-l.ch)
			}
			if f != nil {
				f.Close()
				f = nil
			}
			close(done)
		}
	}
}
