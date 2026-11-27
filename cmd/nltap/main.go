// nltap sits in front of llama-server and says who is calling.
//
// It listens on one port per caller, forwards every request unchanged to
// llama-server, streaming included, and appends one line per request to a log
// that nltop reads: who, start, end, and the timings llama-server returned.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wjk22/neo-llama-top/internal/tap"
)

// version is set by ./build, e.g. "0.1.7 (3bc09b7)".
var version = "unversioned build"

type caller struct {
	addr string // listen address
	name string
}

type config struct {
	upstream *url.URL
	logPath  string
	callers  []caller
}

// parseConfig reads the config file: "key = value" lines, # for comments.
//
//	upstream = http://127.0.0.1:8080
//	log      = /var/log/nltap/requests.jsonl
//	6081     = Motoko
//	6082     = Rico
//
// Any other key is a listen address and its value the caller's name. A bare
// port listens on 127.0.0.1.
func parseConfig(r io.Reader) (config, error) {
	cfg := config{logPath: tap.DefaultLog}
	upstream := "http://127.0.0.1:8080"
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !ok || key == "" || val == "" {
			return cfg, fmt.Errorf("line %d: want \"key = value\", got %q", n, line)
		}
		switch key {
		case "upstream":
			upstream = val
		case "log":
			cfg.logPath = val
		default:
			addr := key
			if _, err := strconv.Atoi(key); err == nil {
				addr = "127.0.0.1:" + key
			}
			if _, port, err := net.SplitHostPort(addr); err != nil || port == "" {
				return cfg, fmt.Errorf("line %d: %q is neither a setting (upstream, log) nor a port or address to listen on", n, key)
			}
			if seen[addr] {
				return cfg, fmt.Errorf("line %d: %s is listed twice", n, addr)
			}
			seen[addr] = true
			cfg.callers = append(cfg.callers, caller{addr: addr, name: val})
		}
	}
	if err := sc.Err(); err != nil {
		return cfg, err
	}
	u, err := url.Parse(upstream)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return cfg, fmt.Errorf("upstream %q is not an http(s) URL", upstream)
	}
	cfg.upstream = u
	if len(cfg.callers) == 0 {
		return cfg, errors.New("no caller listed: add a line like \"6081 = Motoko\"")
	}
	return cfg, nil
}

func main() {
	configPath := flag.String("config", "/etc/nltap.conf", "config file: upstream, log, and one \"port = name\" line per caller")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "nltap %s\n\nUsage: nltap [flags]\n\n", version)
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("nltap", version)
		return
	}
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "nltap:", err)
		os.Exit(1)
	}

	f, err := os.Open(*configPath)
	if err != nil {
		fail(err)
	}
	cfg, err := parseConfig(f)
	f.Close()
	if err != nil {
		fail(fmt.Errorf("%s: %w", *configPath, err))
	}

	// Take every port before serving on any, so a port that is in use stops
	// nltap at once instead of leaving one caller without a door.
	var listeners []net.Listener
	for _, c := range cfg.callers {
		ln, err := net.Listen("tcp", c.addr)
		if err != nil {
			fail(err)
		}
		listeners = append(listeners, ln)
	}

	lg := newLogger(cfg.logPath)
	lg.log(tap.Event{Ev: tap.Up, Start: time.Now()})
	t := newTap(cfg.upstream, lg)

	var servers []*http.Server
	for i, c := range cfg.callers {
		srv := &http.Server{Handler: t.handler(c.name), ReadHeaderTimeout: 30 * time.Second}
		servers = append(servers, srv)
		go srv.Serve(listeners[i])
		fmt.Fprintf(os.Stderr, "nltap: %s is %s\n", c.addr, c.name)
	}
	fmt.Fprintf(os.Stderr, "nltap %s: forwarding to %s, logging to %s\n", version, cfg.upstream, cfg.logPath)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	// Give running answers a moment to finish, then go.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Go(func() { srv.Shutdown(ctx) })
	}
	wg.Wait()
	lg.close()
}
