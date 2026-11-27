# nltop

A terminal monitor for llama.cpp's `llama-server`, in the style of [btop](https://github.com/aristocratos/btop): what the server is doing right now, how fast, how full the context is, what it did today, and what the GPU makes of it.

nltop only watches. It reads the server with `GET` requests, the request history from the server's journal, and the GPU from NVML. It works without `--metrics`.

## Install

You need Go 1.26 or newer to build. The result is one static binary.

```sh
git clone https://github.com/wjk22/neo-llama-top.git
cd neo-llama-top && ./install
```

`./install` builds `builds/nltop` and copies it to `/usr/local/bin`, asking for sudo if it has to. `PREFIX=$HOME/.local ./install` puts it in `~/.local/bin` instead. To build on one machine and run on another, run `./build` and copy `builds/nltop` over; it has no dependencies.

## Run

```sh
nltop                                   # llama-server on http://127.0.0.1:8080, unit llama-server
nltop -url http://127.0.0.1:9000 -unit my-llama
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-url` | `http://127.0.0.1:8080` | base URL of llama-server |
| `-unit` | `llama-server` | systemd unit whose journal holds the request history |
| `-interval` | `500ms` | time between two readings of `/slots` |
| `-api-key` | `$LLAMA_API_KEY` | API key, if the server was started with one |
| `-version` | | print the version and exit |

Keys: `1`–`5` show or hide a panel, `↑` `↓` `PgUp` `PgDn` `Home` `End` scroll the request list, `q` quits. The terminal has to be at least 80×24.

Run it on the machine the server runs on. The journal and the GPU are read locally, and reading the system journal needs root or membership of the `systemd-journal` group.

## The five panels

| Key | Panel | Shows |
| --- | --- | --- |
| `1` | server | state (ok, loading model, sleeping, unreachable), model, quantisation, parameters, file size, context size, slots, build, uptime |
| `2` | slots | per slot: state, context gauge, prompt and generation progress with speeds, sampler settings. While the server is idle: the last request and today's totals |
| `3` | tokens | braille graph of generated tokens per second (upwards) and prompt tokens per second (downwards), with current, peak and today's figures |
| `4` | requests | one line per request, newest first: start, caller, duration, prompt tokens, cached tokens, speeds, generated tokens, context used |
| `5` | gpu | utilisation with history, memory, temperature, power, fan |

A hidden panel gives its room to its neighbour.

The context gauge runs from teal to amber, so a context that fills up shades towards amber.

When the server goes idle, the graph does not scroll the last burst away. Its time window stretches, up to three times the normal span. After that the picture is held, with the burst in the left fifth, and only the idle time next to it keeps counting.

## Where the numbers come from

| Source | Used for |
| --- | --- |
| `GET /health` | server state |
| `GET /props` | model alias, quantisation, slots, modalities, build, sleeping, whether `/metrics` exists |
| `GET /v1/models` | parameter count, file size, trained context size |
| `GET /slots` | slot state, context fill, prompt and generation counters, sampler settings |
| `GET /metrics` | only if the server runs with `--metrics`: queued requests and totals since start |
| journal of the unit | request history: start, end, token counts, speeds, context at the end; server uptime |
| NVML, through `nvidia-smi` | GPU readings |

**Throughput** is the difference between two readings of the slot counters, divided by the time between them. Nothing else is needed, so the graph works without `--metrics`. The prompt counter moves a whole batch at a time, seconds apart; nltop spreads each batch over the time it took instead of drawing a spike.

**Request history** comes from the lines llama-server logs for every request. nltop reads the journal from yesterday 00:00 on, so the last request and today's totals are there as soon as it starts.

**Cached tokens** of a finished request are worked out: tokens in the context at the end, minus the tokens that request evaluated and generated. Checked against the server's own figure, it matches.

## Thinking and answering

nltop has two colours for generation: violet for thinking, green for answering.

`/slots` does not say which of the two a slot is writing. It reports one counter for all generated tokens, and nothing in it changes when the model stops reasoning and starts its answer. That split exists only in the response stream, which a monitor that only reads does not see. (Checked on build `b1-1715275`, in the source and against a live server.)

So nltop shows what it can know:

- **answering** (green) when the request has thinking switched off. `/slots` shows the generation prompt, and with thinking off the template closes the think block there. Everything generated is then the answer.
- **generating** (grey) when thinking is on. The tokens are counted and timed; which of them are reasoning is not known, and nltop does not guess.

Violet is reserved for a source that sees the stream.

## The "who" column

The request list has a column for the caller. It shows `–` for now: neither `/slots` nor the journal names who sent a request. It is there for a small pass-through in front of the server that knows its callers.

## Limits

- A request shorter than one poll interval is in the request list but may not show in the graph.
- Request history needs llama-server to run as a systemd unit and log at its default verbosity.
- GPU readings are NVIDIA only. A static binary cannot load the NVML library itself, so nltop keeps one `nvidia-smi` process open, which reads NVML and prints a line per second.
- One server per nltop. Start a second one for a second server.

## Development

See [AGENTS.md](AGENTS.md).

```sh
./build && go vet ./... && go test ./...
```

## Licence

MIT, see [LICENSE](LICENSE).
