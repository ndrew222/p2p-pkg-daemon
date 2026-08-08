# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Read these before writing anything

Two files govern work here and both are binding:

1. **`AGENTS.md`** — ground rules, document precedence, and the hard-constraints list. Written for agents specifically.
2. **`docs/logs/HANDOFF.md`** — current state: what is done, what is blocked on an owner ruling, what is stale, and what the next work item is.

The four rules from `AGENTS.md` that actually change how you work:

- **Spec first.** Every change must map to a use-case step, a spec, or an ADR. No spec covers it → stop, do not write the code.
- **Ambiguity means stop and ask.** If a spec is silent, unclear, or self-contradictory on something your task needs, report it and wait. Do not pick a reasonable interpretation and continue. The specs' *Deliberately unspecified — do not invent, ask* tables are not gaps for you to fill.
- **Work log per feature**, at `docs/logs/<author>-<feature>.md`: how you approached it, difficulties, and every area of uncertainty with whether you raised it and with whom. Silently resolving an uncertainty yourself is a spec violation.
- **End the session by stating what is waiting on the owner** — every ambiguity raised, every ruling deferred, anything delivered that assumes an answer you did not get, plus what unblocks it and what it costs to leave it blocked. A session with no such items should say so.

Commits are reviewed one at a time: **one reviewable change per commit**, message explains *why*.

## Build, test, verify

```sh
go build ./... && go vet ./... && go test ./...   # the gate — no CI exists
gofmt -l .                                        # must print nothing; no linter is configured
```

```sh
go test ./internal/daemon/                                  # one package
go test ./internal/daemon/ -run TestFacadeStatusCodes -v     # one test
go test ./internal/peerwire/ -run FuzzReadMessage -fuzz FuzzReadMessage  # fuzz (corpus in testdata/)
plantuml -checkonly docs/uc-02.puml                          # before committing a diagram
```

Tracker code and tests **must run on any OS** — no FreeBSD dependency, no `pkg`, no second machine. The "Definition of done" section in each spec is that component's test list; write those table-driven.

Running the daemon (see `cmd/jmj/README.md` for the full flag list):

```sh
go run ./cmd/trac                                        # tracker, :8080
go run ./cmd/jmj -generate-config > ~/.config/jmj/config.json
go run ./cmd/jmj -config ~/.config/jmj/config.json       # SIGHUP reloads
```

`-generate-config` prints to stdout and touches no filesystem; the shell does the writing. There is no config writer in the codebase and no permission handling — **do not add one**.

## Architecture

FreeBSD peer-to-peer package distribution. `pkg` is never modified, wrapped, or patched; the daemon integrates by impersonating a **mirror** over HTTP.

Two binaries — `cmd/trac` (tracker, one per swarm) and `cmd/jmj` (daemon, one per host) — and both are thin `main`s over `internal/`.

### Three wires that do not share a path grammar

This is the single most important structural fact, and "unifying" them is an explicitly listed trap.

| Wire | Path surface | Contract |
|---|---|---|
| daemon ↔ tracker | `POST /announce`, `POST /ping`, `GET /peers?pkg=<name-version>` | `docs/tracker-protocol-spec-v0.2.md` (encoding) + `docs/protocol-spec-v0.1.md` (semantics) |
| pkg → daemon (facade) | `…/All/[Hashed/]<name-version>[~hash10].pkg` | **ADRs only — no spec file.** ADR-003 (fetch semantics, status codes), ADR-004 (path rule) |
| daemon ↔ daemon (peer) | `GET /pkg/<name-version>` | `docs/peer-transfer-spec-v0.2.md` |

The peer namespace is deliberately unlike the facade's so that a seeding daemon cannot be mistaken for, or used as, a pkg mirror.

### Package flow

`pkg` requests a package from the facade (loopback only) → facade asks the tracker who holds it → fetches from a peer over the peer wire, spooling through `temp_dir` and hashing incrementally → verifies against the expected SHA-256 and exact size **from pkg's own repository database** → serves the bytes to `pkg` and deletes the spool. On a peer miss, ADR-003 says proxy to a configured upstream mirror and stream through (not yet implemented — see below). Meanwhile the cache watcher (`fsnotify` on `cache_dir`, read-only) tells the tracker what this host can serve.

### Packages

- `internal/config` — load / validate / generate-to-stdout. Two listen addresses: `facade_addr` (**loopback-enforced**, the daemon refuses to start otherwise — reachable off-host it is an open bandwidth relay) and `serving_addr` (public; its port is what gets announced). Plus `temp_dir` (scratch, not a cache), `cache_dir` and `repo_db_dir` (both read-only, must already exist, never created).
- `internal/proto` — message types, JSON encoding, validation. Shared by both sides.
- `internal/tracker` — in-memory peer table keyed IP:port, expiry sweeper. Never relays bytes, never verifies content.
- `internal/discovery` — tracker client plus the keep-alive loop. Announce lists are **always full replacements, never deltas**.
- `internal/daemon` — `daemon.go` (lifecycle/wiring), `watcher.go` (cache watcher), `repodb.go` (`Repositories`: read-only snapshot of every SQLite catalogue under `repo_db_dir`, via `modernc.org/sqlite`), `facade.go` (the pkg-facing handler), `repository.go` (composite of the narrow interfaces).
- `internal/peer` — `fetch.go`/`download.go` (requester), `serve.go` (seeder), `blacklist.go` (local-only, in-memory, no expiry, whole-peer; nothing is reported to the tracker).
- `internal/peerwire` — **deprecated**, interim length-prefixed binary framing. To be deleted, not extended.

Do not create new top-level directories without asking.

### Verification model

Integrity comes from exactly one place: pkg's repository database (`ExpectedHash`, `ExpectedFileSizeBytes`). The tracker never verifies content and peers are not trusted. No hashing at announce time — sanity checks only; the downloader verifies. Only a hash mismatch blacklists a peer; a dial failure never does.

The daemon writes **only** to its own `temp_dir`. The pkg cache and the repository database are read-only — the watcher once called `MkdirAll` on the cache dir and that was a hard-constraint violation.

## Current state — read HANDOFF.md before picking anything up

- **`internal/daemon/facade.go` is frozen (HANDOFF §5.7).** It implements a model that was *measured false*: it returns an HTTP error on a peer miss, assuming pkg falls through to another mirror. pkg does not — fall-through happens between mirrors within a repository, never between repositories, so a facade error ends the install. **Its tests pass, which is misleading**: they encode the old contract, so green means consistently wrong, not correct. Blocked on owner rulings §4.4 (does the facade proxy pkg's catalogue?) and §4.5 (how the upstream mirror is configured). Do not extend, tune, or partially migrate it. The path rule in that file is unaffected and correct — do not "fix" it.
- **The seed half does not exist.** No production `PackageSource` implementation is in the tree; the only implementors are test fakes and `cmd/demo`'s in-memory store. The daemon announces a serving port nothing listens on.
- **§5.3 (peer wire migration) is the next work item and is fully unblocked** — and it is a build, not just a migration. Deleting `internal/peerwire` before its size bound is replaced is explicitly forbidden: `MaxPayload` is today the only length check on the fetch path.
- `docs/mirror-facade-spec-v0.1.md` is **deprecated** — history only, never binding, superseded by ADR-003/ADR-004. There is no v0.2 and none is planned. Do not implement from it or cite it as a contract.
- Deprecated vocabulary: anything referencing IPFS, CIDs, or `peer_id`. Packages are addressed by `name-version` (e.g. `nginx-1.24.0_2`). Flag such code if you find it; do not extend it.

## Traps with a history

Each of these has already cost someone a session (full list in `AGENTS.md` and HANDOFF §8):

- **No global package size cap.** The bound is the exact expected size from the repo DB, which is stricter than any constant and has no ceiling. The constant that used to exist blocked 1.3% of the repository outright — llvm, rust, chromium, libreoffice.
- **Constant memory on both ends of a transfer.** The requester streams to a temp file and hashes incrementally; the seeder serves from an open file handle. A `[]byte` in either signature is a regression, and is what currently OOMs a 1 GiB host on the 2.83 GiB package.
- **No stall detectors, minimum-throughput rules, or transfer deadlines.** A slow peer is out of scope exactly as a slow mirror is. No throttling, bandwidth management, or NAT traversal either (ADR-001) — though that is a scope ruling, not a ban on thinking: if you observe a real problem rate control solves, make the case openly rather than smuggling one in, and equally do not cite it as a reason to leave a genuine defect unfixed.
- **A nil `*Repositories` assigned into an interface field is a non-nil interface holding a nil pointer** — every `== nil` check downstream passes and the first call panics. Both wiring sites go through `Daemon.repository()` for this reason; `TestStartHTTPServerRefusesWithoutARepositoryDatabase` is the regression test.
- **PlantUML:** square brackets in an `alt`/`else` label parse as a link and eat the first word; angle brackets in a note body parse as markup. Render and look at it before committing.
- **Do not claim a fact about a system you have not inspected.** An earlier session invented a cache layout and the owner caught it. A plausible self-attributed cause is not a control either.

## Conventions

- Section numbers (`§4.4`, `§5.3`, …) are cited across ADRs, work logs, commit messages **and Go source comments**. Do not renumber them and do not recycle a retired number — HANDOFF's opening table maps the retired ones.
- Source comments carry contract pointers and blocker markers; `grep -rn 'BLOCKED (HANDOFF §5.7)' internal/` finds the frozen code. Keep that style when you add constraints.
- If you resolve something in HANDOFF.md, edit it in the same commit as the work.
- `docs/` is spec territory — agents do not modify specs. `docs/logs/` is where agent output goes.

Module path `github.com/ndrew222/p2p-pkg-daemon`, Go 1.26. There is no root `README.md`, despite `AGENTS.md` referring to one.

**Note on graphify:** HANDOFF §0 mandates `graphify query "<question>"` before reading source and `graphify update .` after changing it, citing this file. The installed `graphify` exposes no `query` or `update` subcommands (it has `path`, `explain`, `diagnose`, `merge-graphs`) and this repository has no `graphify-out/graph.json`, so that workflow is not currently runnable. Read and grep source directly; HANDOFF's own caveat already allows for the tool being absent.
