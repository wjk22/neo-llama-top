// nltop is a terminal monitor for llama.cpp's llama-server, in the style of btop.
//
// It reads the server with GET requests only (/health, /props, /slots,
// /v1/models and, when enabled, /metrics), takes the request history from the
// server's journal and GPU readings from NVML.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/wjk22/neo-llama-top/internal/tap"
)

// version is set by ./build: MAJOR.MINOR from the VERSION file, the commit
// count since it last changed, and the commit, e.g. "0.2.7 (3bc09b7)".
var version = "unversioned build"

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "base URL of llama-server")
	unit := flag.String("unit", "llama-server", "systemd unit whose journal holds the request history")
	tapLog := flag.String("tap", tap.DefaultLog, "nltap's log, which names the callers")
	interval := flag.Duration("interval", 500*time.Millisecond, "time between two readings of /slots")
	apiKey := flag.String("api-key", "", "API key, if the server was started with one (default $LLAMA_API_KEY)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "nltop %s\n\nUsage: nltop [flags]\n\n", version)
		flag.PrintDefaults()
		fmt.Fprint(flag.CommandLine.Output(), "\nKeys: 1-5 show or hide a panel · ↑ ↓ PgUp PgDn Home End scroll the requests · q quit\n")
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("nltop", version)
		return
	}
	if *apiKey == "" {
		*apiKey = os.Getenv("LLAMA_API_KEY") // read here, so that -h never prints the key
	}
	if *interval < 100*time.Millisecond {
		fmt.Fprintln(os.Stderr, "nltop: -interval must be at least 100ms")
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := tea.NewProgram(newModel(config{url: *url, unit: *unit, tapLog: *tapLog, interval: *interval, version: version}))
	go poll(ctx, newClient(*url, *apiKey), *interval, func(m pollMsg) { p.Send(m) })
	go followJournal(ctx, *unit, func(m journalMsg) { p.Send(m) })
	go followTap(ctx, *tapLog, *interval, func(m tapMsg) { p.Send(m) })
	go followGPU(ctx, func(m gpuMsg) { p.Send(m) })

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "nltop:", err)
		os.Exit(1)
	}
}
