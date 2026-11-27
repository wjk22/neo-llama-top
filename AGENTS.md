# AGENTS.md

Notes for anyone, human or AI, who changes this repository.

## What this is

`nltop` is a terminal monitor for llama.cpp's `llama-server`, in the style of btop: package `main` in the repository root. `nltap` is a small pass-through in front of llama-server that notes who is calling: package `main` in `cmd/nltap`. README.md describes what they show and where each number comes from.

## Ground rules

- **nltop is read only.** It sends nothing but `GET` to `/health`, `/props`, `/slots`, `/v1/models` and `/metrics`. It never posts, never changes server settings. A test fails if any other method is sent.
- **nltap changes nothing and never gets in the way.** Requests and answers pass through as they are, streams chunk by chunk. It logs who, when and llama-server's timings, never prompts, answers or headers. Writing the log must not block or fail a request: if the log can't be written, the line is lost and the request is still forwarded.
- **Show what the source says.** A number on screen comes from a named source: slot counters, the journal, NVML. Where a source cannot tell something (see "Thinking and answering" in the README), the screen says so or leaves it out. No estimates dressed up as readings.
- **Static binaries.** `CGO_ENABLED=0`, no runtime libraries. Outside help is limited to two programs that are already on the machine: `journalctl` and `nvidia-smi`.
- **Simple over clever.** No new dependency, package split or abstraction without a need the current code cannot meet.

## Layout

| File | What it holds |
| --- | --- |
| `main.go` | flags, version, starting the readers |
| `llama.go` | HTTP client and the poll loop |
| `journal.go` | `journalctl` follower and the log-line parser |
| `gpu.go` | `nvidia-smi` reader |
| `tap.go` | reader of nltap's log; joins callers to requests |
| `series.go` | throughput from slot-counter differences, the graph's time window |
| `model.go` | program state and key handling |
| `view.go` | screen layout and the five panels |
| `draw.go`, `theme.go` | boxes, meters, braille graphs, number formats, colours |
| `testdata/` | answers and journal lines captured from a real server |
| `cmd/nltap/` | nltap: config, the forwarding handler, the log writer |
| `internal/tap/` | the format of nltap's log, shared by both programs |
| `build`, `install` | build script; guided installer that also sets up the nltap service |

## Build and test

```sh
./build          # builds/nltop and builds/nltap, static, version stamped
go vet ./...
go test ./...
```

Tests use the captured data in `testdata/`; they need no server, no journal and no GPU. To try the installer without touching the machine: `DESTDIR=/some/empty/dir ./install`. When a change touches what a panel shows, run nltop against a real server too and look at it: at 80×24, at a large size, and with panels switched off.

## Version

`VERSION` holds `MAJOR.MINOR` and is changed by hand. The third number is the count of commits since `VERSION` last changed, that commit being 1. `./build` works it out from git, so nothing else stores a version. A build shows `0.1.7 (3bc09b7)`, or `0.1.7+dev (3bc09b7)` with uncommitted changes.

## Git

- Work on `main`. No feature branches.
- If a branch or worktree exists anyway: delete it, locally and on origin, once it is merged or no longer used.
- Leave the working tree clean. Say what you left uncommitted, and why.

## Housekeeping

- `builds/` is build output and `temp/` is scratch. Both are ignored by git, as is `install-answers.toml`; never commit from them.
- Things worth keeping for a while (screenshots, measurements) go into `temp/`, not the system's temp folder.
- Before you finish, remove the scratch you made: files in `temp/`, helper scripts, test binaries. Leave files someone else put there, and name them in your report. Go compiles stray `.go` files in ignored folders too, so do not leave any.
- No task lists, status notes or review records in the repository. It holds code, build scripts and documentation of the program as it is now. Comments describe behaviour, not tickets.
