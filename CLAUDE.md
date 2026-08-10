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
go test ./... -race -count=2                      # before requesting a merge; see below
```

`-race` is **not** part of the gate but is required before a merge request. §5.3
merged green with a data race the plain gate cannot see, found only when a later
change ran `-race`, and intermittent even then. Two HTTP servers, two
semaphores, a watcher goroutine and a SIGHUP reload path now share this process.

```sh
go test ./internal/daemon/                                  # one package
go test ./internal/daemon/ -run TestFacadeStatusCodes -v     # one test
go test ./internal/peer/ -run FuzzSeederHTTPSurface -fuzz FuzzSeederHTTPSurface  # fuzz the seeder's HTTP surface
plantuml -checkonly docs/uc-02.puml                          # before committing a diagram
```

Tracker code and tests **must run on any OS** — no FreeBSD dependency, no `pkg`, no second machine. The "Definition of done" section in each spec is that component's test list; write those table-driven.

`TESTING.md` is the map: which layer proves what, which use case each test belongs to, what the adversarial cases are, and — the part worth reading before you claim coverage — what is deliberately **not** tested and why.

Running the daemon (see `cmd/jmj/README.md` for the full flag list):

```sh
go run ./cmd/trac                                        # tracker, :8080
go run ./cmd/jmj -generate-config -upstream 'https://pkg.FreeBSD.org/${ABI}/quarterly' > ~/.config/jmj/config.json
go run ./cmd/jmj -config ~/.config/jmj/config.json       # SIGHUP reloads
```

`-generate-config` prints to stdout and touches no filesystem; the shell does the writing. There is no config writer in the codebase and no permission handling — **do not add one**.

## Architecture

FreeBSD peer-to-peer package distribution. `pkg` is never modified, wrapped, or patched; the daemon integrates by impersonating a **mirror** over HTTP.

Two binaries — `cmd/trac` (tracker, one per swarm) and `cmd/jmj` (daemon, one per host) — and both are thin `main`s over `internal/`. `cmd/demo` is a third, not shipped: it drives the real peer wire end to end in one process (`TESTING.md`, `docs/logs/claude-demo-guide.md` §1.1).

### Three wires that do not share a path grammar

This is the single most important structural fact, and "unifying" them is an explicitly listed trap.

| Wire | Path surface | Contract |
|---|---|---|
| daemon ↔ tracker | `POST /announce`, `POST /ping`, `GET /peers?pkg=<name-version>` | `docs/tracker-protocol-spec-v0.2.md` (encoding) + `docs/protocol-spec-v0.1.md` (semantics) |
| pkg → daemon (facade) | `…/All/[Hashed/]<name-version>[~hash10].pkg`, plus every non-package path | **ADRs only — no spec file.** ADR-003 (fetch semantics, status codes), ADR-004 (path rule, `GET`-only), ADR-005 (metadata is proxied), ADR-006 (`upstream_url`), ADR-007 (jmj fronts one repository), ADR-009 (the status set is exhaustive; no `500`), ADR-010 (own catalogue wins a collision) |
| daemon ↔ daemon (peer) | `GET /pkg/<name-version>` | `docs/peer-transfer-spec-v0.2.md` |

The peer namespace is deliberately unlike the facade's so that a seeding daemon cannot be mistaken for, or used as, a pkg mirror.

### Package flow

`pkg` requests a package from the facade (loopback only) → facade asks the tracker who holds it → fetches from a peer over the peer wire, spooling through `temp_dir` and hashing incrementally → verifies against the expected SHA-256 and exact size **from pkg's own repository database** → serves the bytes to `pkg` and deletes the spool. On a peer miss — tracker unreachable, empty peer list, every holder blacklisted, every holder tried and failed, or even an unwritable `temp_dir` — the facade fetches from `upstream_url` and streams straight through, with no spool and no cache (ADR-003, ADR-009). None of those five is visible to pkg; they are distinguished in the log and nowhere else. Meanwhile the cache watcher (`fsnotify` on `cache_dir`, read-only) tells the tracker what this host can serve, and the repository watcher (`fsnotify` on `repo_db_dir`, read-only) reloads the catalogue when pkg rewrites it and re-announces against the new one.

### Packages

- `internal/config` — load / validate / generate-to-stdout. Two listen addresses: `facade_addr` (**loopback-enforced**, the daemon refuses to start otherwise — reachable off-host it is an open bandwidth relay) and `serving_addr` (public; its port is what gets announced). Plus `temp_dir` (scratch, not a cache), `cache_dir` and `repo_db_dir` (both read-only, must already exist, never created), and `upstream_url` — the one **required** key with **no default** (ADR-006), because it decides which repository pkg installs from. `${ABI}` in it is expanded at startup only, via the daemon's one `exec` of `pkg config abi`, and only when the placeholder is present.
- `internal/proto` — message types, JSON encoding, validation. Shared by both sides.
- `internal/tracker` — in-memory peer table keyed IP:port, expiry sweeper. Never relays bytes, never verifies content.
- `internal/discovery` — tracker client plus the keep-alive loop. Announce lists are **always full replacements, never deltas**.
- `internal/daemon` — `daemon.go` (lifecycle/wiring, SIGHUP reload, both HTTP servers), `watcher.go` (cache watcher), `repodb.go` (`Repositories`: read-only snapshot of every SQLite catalogue under `repo_db_dir`, via `modernc.org/sqlite`; ADR-010 prefers our own repository's row on a collision), `repowatcher.go` (ADR-008: `fsnotify` on `repo_db_dir` **directories**, two-second settle, `lock`/`meta` ignored, a failed reload keeps the old snapshot and a successful one re-announces), `facade.go` (the pkg-facing handler), `upstreamcheck.go` (ADR-006's **advisory** cross-check: does `upstream_url` match any repository pkg actually fetches from, read from the catalogue's own `repodata` table — it warns and never refuses), `repository.go` (composite of the narrow interfaces).
- `internal/peer` — the daemon↔daemon wire (`docs/peer-transfer-spec-v0.2.md`). `fetch.go`/`download.go` (requester: streams to a temp file, hashes incrementally, returns an open `*os.File`), `serve.go` (seeder: an `http.Server` over `PackageSource`, serving from open handles via `http.ServeContent`), `limit.go` (ADR-002's two non-blocking semaphores and the `503`), `blacklist.go` (local-only, in-memory, no expiry, whole-peer; nothing is reported to the tracker).
- `internal/daemon/cachesource.go` — the production `peer.PackageSource`: opens `<cache_dir>/<name-version>.pkg` read-only. It is also the path-safety boundary, because `peer.validName` deliberately is not one.
- `internal/peerwire` — **deleted** with the v0.2 wire. Do not reintroduce it.

Do not create new top-level directories without asking.

### Verification model

Integrity comes from exactly one place: pkg's repository database (`ExpectedHash`, `ExpectedFileSizeBytes`). The tracker never verifies content and peers are not trusted. No hashing at announce time — sanity checks only; the downloader verifies. Only a hash mismatch blacklists a peer; a dial failure never does.

The daemon writes **only** to its own `temp_dir`. The pkg cache and the repository database are read-only — the watcher once called `MkdirAll` on the cache dir and that was a hard-constraint violation.

## Current state — read HANDOFF.md before picking anything up

- **Everything in §5 is done and ADR-001 through ADR-010 are all Approved.** §5.1 (config schema), §5.2 (repository database reader and ADR-008's reload trigger), §5.3 (the HTTP peer wire and the cache-backed seeder), §5.4 (mounting both servers), §5.5 (the blacklist) and §5.7 (the facade rework, 2026-08-09). **There is no large open work item and nothing is with the owner as a question.**
- **`internal/daemon/facade.go` is no longer frozen** — the model it once implemented was measured false and was rewritten, not edited. It now fetches from `upstream_url` on a peer miss (ADR-003), relays every non-package path including `If-Modified-Since`/`304` (ADR-005), and its status set is exhaustive with no `500` (ADR-009). The tests were rewritten with it rather than deleted. The path rule in that file is ADR-004's, is measured against pkg 2.7.5, and is correct — do not "fix" it.
- **The `400`-versus-`404` question on the peer wire was ruled `400` (2026-08-10)**, which is what shipped, and `docs/peer-transfer-spec-v0.2.md` now says so in both places. A `404` carries the UC-06 §5b re-announce obligation, so answering one to a malformed path would let a hostile peer drive our announce traffic.
- **The system has been run end to end against real pkg** — `pkg update` and a real `pkg install` through the facade on a FreeBSD host (HANDOFF §7.6–§7.9), and a three-machine trial including a hostile peer and 98 MB moved at constant memory (§7.10–§7.11). `TESTING.md` is the map of what is tested and how; `docs/logs/claude-demo-guide.md` carries the dated transcripts.
- What is still unproven is narrow and listed at `claude-demo-guide.md` §3.4.1: a genuinely slow link, an interrupted transfer, more than three holders.
- `docs/mirror-facade-spec-v0.1.md` is **deprecated** — history only, never binding, superseded by ADR-003/ADR-004. There is no v0.2 and none is planned. Do not implement from it or cite it as a contract.
- Deprecated vocabulary: anything referencing IPFS, CIDs, or `peer_id`. Packages are addressed by `name-version` (e.g. `nginx-1.24.0_2`). Flag such code if you find it; do not extend it.

## Traps with a history

Each of these has already cost someone a session (full list in `AGENTS.md` and HANDOFF §8):

- **No global package size cap.** The bound is the exact expected size from the repo DB, which is stricter than any constant and has no ceiling. The constant that used to exist blocked 1.3% of the repository outright — llvm, rust, chromium, libreoffice.
- **Constant memory on both ends of a transfer.** The requester streams to a temp file and hashes incrementally; the seeder serves from an open file handle. A `[]byte` in either signature is a regression, and is what currently OOMs a 1 GiB host on the 2.83 GiB package.
- **No stall detectors, minimum-throughput rules, or transfer deadlines.** A slow peer is out of scope exactly as a slow mirror is. No throttling, bandwidth management, or NAT traversal either (ADR-001) — though that is a scope ruling, not a ban on thinking: if you observe a real problem rate control solves, make the case openly rather than smuggling one in, and equally do not cite it as a reason to leave a genuine defect unfixed.
- **A successful repository-database lookup is not proof the upstream can serve that package.** `repo_db_dir` is scanned for *every* catalogue, but jmj fronts exactly one repository (ADR-007), so `Repositories` will happily return a hash for a package `upstream_url` cannot fetch. pkg never asks for those, so the path is unreachable — but the two predicates are not the same, and the facade does not conflate them: `serveUpstreamPackage`'s non-200 branch exists rather than being asserted away. Do not "simplify" it out.
- **A nil `*Repositories` assigned into an interface field is a non-nil interface holding a nil pointer** — every `== nil` check downstream passes and the first call panics. Both wiring sites go through `Daemon.repository()` for this reason; `TestStartHTTPServerRefusesWithoutARepositoryDatabase` is the regression test.
- **PlantUML:** square brackets in an `alt`/`else` label parse as a link and eat the first word; angle brackets in a note body parse as markup. Render and look at it before committing.
- **Do not claim a fact about a system you have not inspected.** An earlier session invented a cache layout and the owner caught it. A plausible self-attributed cause is not a control either.

## Conventions

- Section numbers (`§4.4`, `§5.3`, …) are cited across ADRs, work logs, commit messages **and Go source comments**. Do not renumber them and do not recycle a retired number — HANDOFF's opening table maps the retired ones.
- Source comments carry contract pointers, and blocker markers when something is genuinely blocked. **There are none in `internal/` now** — the `BLOCKED (HANDOFF §5.7)` and `SUPERSEDED (HANDOFF §5.7)` markers that older commit messages mention were removed with the rework and find nothing. Keep that style when you add a constraint, and remove the marker in the commit that unblocks it.
- If you resolve something in HANDOFF.md, edit it in the same commit as the work.
- `docs/` is spec territory — agents do not modify specs. `docs/logs/` is where agent output goes. One commit on 2026-08-10 does edit specs: it applied HANDOFF §9's recorded backlog under a one-off owner permission, and it is not a precedent. Record what needs changing in HANDOFF §9 and ask.

Module path `github.com/ndrew222/p2p-pkg-daemon`, Go 1.26. Orientation for a human lives in `README.md`; how the thing is tested lives in `TESTING.md`.

**Note on graphify:** the installed binary does expose `query`, `update`, `path` and `explain` — an earlier note in this file claimed otherwise and was wrong. What the workflow actually depends on is `graphify-out/graph.json`, which **this repository does not have yet**; a build was in flight on 2026-08-10 and left no graph behind. So the rules below are conditional exactly as they are written: run `graphify query` *when the graph exists*, and read or grep source directly when it does not. Check before assuming either way.

## graphify

This project keeps a knowledge graph at graphify-out/ — god nodes, community structure, cross-file relationships — **once one has been built.** As of 2026-08-10 there is no `graphify-out/graph.json` here, so every rule below is conditional on the file existing; check first.

Rules:
- For codebase questions, first run `graphify query "<question>"` when graphify-out/graph.json exists. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
