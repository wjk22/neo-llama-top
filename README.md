# nltop

A terminal monitor for llama.cpp's `llama-server`, in the style of [btop](https://github.com/aristocratos/btop): what the server is doing right now, how fast, how full the context is, what it did today, and what the GPU makes of it.

nltop only watches. It reads the server with `GET` requests, the request history from the server's journal, and the GPU from NVML. It works without `--metrics`.

`nltap`, a small second program, tells nltop **who is calling**. It sits in front of llama-server with one port per caller, passes everything through unchanged, and notes who sent each request.

## Install

You need Go 1.26 or newer to build. The result is two static binaries, `nltop` and `nltap`.

```sh
git clone https://github.com/wjk22/neo-llama-top.git
cd neo-llama-top && ./install
```

`./install` builds both, then asks a few questions, each with a suggested answer that Enter accepts:

- where to install (suggested: `/usr/local`),
- whether to run nltap as a systemd service (suggested: yes),
- where llama-server is (suggested: `http://127.0.0.1:8080`),
- each caller's port and name (suggested: `6081` Motoko, `6082` Rico).

It then shows a summary and waits for `yes`, `change` or `quit`. Nothing on the machine changes before `yes`. It uses sudo where it must. The answers are kept in `install-answers.toml` and suggested again next time; `./install --yes` takes the suggestions without asking.

With the service, the installer creates the user `nltap`, writes `/etc/nltap.conf` and `nltap.service`, and starts it. Then point each caller at its own port instead of llama-server's.

No Go on the target machine? Run `./build` where Go is, copy the checkout with its `builds/` folder over, and run `./install` there. The binaries have no dependencies.

## Run

```sh
nltop                                   # llama-server on http://127.0.0.1:8080, unit llama-server
nltop -url http://127.0.0.1:9000 -unit my-llama
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-url` | `http://127.0.0.1:8080` | base URL of llama-server |
| `-unit` | `llama-server` | systemd unit whose journal holds the request history |
| `-tap` | `/var/log/nltap/requests.jsonl` | nltap's log, which names the callers |
| `-interval` | `500ms` | time between two readings of `/slots` |
| `-api-key` | `$LLAMA_API_KEY` | API key, if the server was started with one |
| `-version` | | print the version and exit |

Keys: `1`–`5` show or hide a panel, `↑` `↓` `PgUp` `PgDn` `Home` `End` scroll the request list, `q` quits. The terminal has to be at least 80×24.

Run it on the machine the server runs on. The journal and the GPU are read locally.

Reading the system journal needs root or membership of the `systemd-journal` group (on Debian `adm` works too). Without it nltop still shows the live panels, and says in ⁴requests that the journal can't be read and how to change that.

## The five panels

| Key | Panel | Shows |
| --- | --- | --- |
| `1` | server | state (ok, loading model, sleeping, unreachable), model, quantisation, parameters, file size, context size, slots, build, uptime |
| `2` | slots | per slot: state, who is calling, context gauge, prompt and generation progress with speeds, sampler settings. While the server is idle: the last request and today's totals |
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
| nltap's log | who called: in ⁴requests, and in ²slots while a request runs |
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

## Who is calling: nltap

llama-server does not say who sent a request: not in `/slots`, not in its journal. nltap finds out by giving every caller a door of its own.

```
Open WebUI (Motoko) ──► 127.0.0.1:6081 ─┐
                                        ├─ nltap ──► llama-server 127.0.0.1:8080
opencode (Rico)     ──► 127.0.0.1:6082 ─┘
```

- **Unchanged traffic.** Method, path, query, headers and body go to llama-server as they came; the answer comes back as llama-server wrote it, chunk by chunk for streams. Only what HTTP asks of a forwarder differs: connection headers such as `Keep-Alive` stay on their hop, and a `Date` header is added when llama-server sent none.
- **A log, not a filter.** For each request nltap appends one line to `/var/log/nltap/requests.jsonl`: who, start, end, method, path, status, and the `timings` object llama-server returned, taken from the JSON answer or from the last chunk of a stream. A `POST` also gets a line when it starts, so the caller is known while it runs. Prompts, answers and headers are never logged.
- **Logging never holds up a request.** Lines are written by a separate writer. If the file can't be written, or the writer falls behind, lines are dropped and the traffic goes on.
- The log is moved aside to `requests.jsonl.1` at 16 MB.

The config file is `/etc/nltap.conf`:

```
upstream = http://127.0.0.1:8080
log      = /var/log/nltap/requests.jsonl

# port = caller. A bare port listens on 127.0.0.1.
6081 = Motoko
6082 = Rico
```

After a change: `sudo systemctl restart nltap`. Requests in flight are cut off by a restart.

**How nltop joins the two.** The journal knows a request by its task number, nltap by its caller. Both carry the same four numbers: prompt tokens, generated tokens, and the two durations to a hundredth of a millisecond. nltop names a request when all four agree. A call that was cut short has no timings; it names the request the server released when the caller hung up. While a request runs, nltop takes the oldest open call for the request that was launched first, because llama-server starts requests in the order they arrive.

A request sent straight to llama-server's own port shows `–` in the who column.

## Limits

- Live speeds are measured between two polls. A prompt batch is timed to the poll that saw it land, so a short batch can read up to one interval too fast or too slow, and the peak with it. Over a whole request the graph adds up to the server's own token count; the request list has the server's exact speeds.
- A request shorter than one poll interval is in the request list but may not show in the graph.
- A caller is named only for requests that went through nltap. If llama-server compresses an answer, nltap cannot read its timings and the request stays unnamed. With several slots, the name shown while requests run assumes they start in the order they arrived.
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
