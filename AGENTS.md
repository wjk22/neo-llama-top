# AGENTS.md

Notes for anyone, human or AI, who changes this repository.

## What this is

`nltop` is a terminal monitor for llama.cpp's `llama-server`, in the style of btop. One Go program, package `main`, in the repository root. README.md describes what it shows and where each number comes from.

## Ground rules

- **Read only.** nltop sends nothing but `GET` to `/health`, `/props`, `/slots`, `/v1/models` and `/metrics`. It never posts, never changes server settings. A test fails if any other method is sent.
- **Show what the source says.** A number on screen comes from a named source: slot counters, the journal, NVML. Where a source cannot tell something (see "Thinking and answering" in the README), the screen says so or leaves it out. No estimates dressed up as readings.
- **One static binary.** `CGO_ENABLED=0`, no runtime libraries. Outside help is limited to two programs that are already on the machine: `journalctl` and `nvidia-smi`.
- **Simple over clever.** No new dependency, package split or abstraction without a need the current code cannot meet.

## Layout

| File | What it holds |
| --- | --- |
| `main.go` | flags, version, starting the three readers |
| `llama.go` | HTTP client and the poll loop |
| `journal.go` | `journalctl` follower and the log-line parser |
| `gpu.go` | `nvidia-smi` reader |
| `series.go` | throughput from slot-counter differences, the graph's time window |
| `model.go` | program state and key handling |
| `view.go` | screen layout and the five panels |
| `draw.go`, `theme.go` | boxes, meters, braille graphs, number formats, colours |
| `testdata/` | answers and journal lines captured from a real server |

## Build and test

```sh
./build          # builds/nltop, static, version stamped
go vet ./...
go test ./...
```

Tests use the captured data in `testdata/`; they need no server, no journal and no GPU. When a change touches what a panel shows, run nltop against a real server too and look at it: at 80×24, at a large size, and with panels switched off.

## Version

`VERSION` holds `MAJOR.MINOR` and is changed by hand. The third number is the count of commits since `VERSION` last changed, that commit being 1. `./build` works it out from git, so nothing else stores a version. A build shows `0.1.7 (3bc09b7)`, or `0.1.7+dev (3bc09b7)` with uncommitted changes.

## Git

- Work on `main`. No feature branches.
- If a branch or worktree exists anyway: delete it, locally and on origin, once it is merged or no longer used.
- Leave the working tree clean. Say what you left uncommitted, and why.

## Housekeeping

- `builds/` is build output and `temp/` is scratch. Both are ignored by git; never commit from them.
- Things worth keeping for a while (screenshots, measurements) go into `temp/`, not the system's temp folder.
- Before you finish, remove the scratch you made: files in `temp/`, helper scripts, test binaries. Leave files someone else put there, and name them in your report. Go compiles stray `.go` files in ignored folders too, so do not leave any.
- No task lists, status notes or review records in the repository. It holds code, build scripts and documentation of the program as it is now. Comments describe behaviour, not tickets.
