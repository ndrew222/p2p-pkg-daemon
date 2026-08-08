# AGENTS.md

Instructions for AI coding agents working in this repository. Human contributors:
read the README and `docs/` instead; this file assumes you are an agent.

## Ground rules

1. **Spec first, always.** The documents in `docs/` are the contract. Every change must map to a use-case step, the tracker protocol spec, or an ADR. If no spec covers what you are about to write, STOP — do not write the code.
2. **Ambiguities are live wires.** If a spec is unclear, contradictory, or silent on something your task needs, STOP AND WAIT. Report the ambiguity to the user you are working with, so they can escalate it to the spec owner. Do not pick a "reasonable" interpretation and continue. Do not code around it.
3. **Deliberately unspecified means ask, not invent.** The specs mark some items as open decisions — see the *Deliberately unspecified — do not invent, ask* tables at the bottom of `docs/protocol-spec-v0.1.md` and `docs/peer-transfer-spec-v0.2.md`. These are not gaps for you to fill.
4. **End by saying what is waiting on the owner.** When you conclude a session, state plainly what is contingent on an owner decision and what is blocked by one: every ambiguity you raised under rules 2 and 3, every ruling you deferred, and anything you delivered that assumes an answer you did not get. For each, say what unblocks it and what it costs to leave it blocked. Rules 2 and 3 only work if what they surface is still visible at the end — an item raised in the middle of a long session and not repeated when you finish has effectively been dropped, and the owner should not have to reread the session to find out what needs them. A session that produced no such items should say that too, rather than leaving it ambiguous.

## Authoritative documents, in precedence order

1. `docs/adr/adr*.md` - Architectural design records. Have the highest priority as they are atomic design decisions vetted and approved by humans with rationale documented.
2. `docs/tracker-protocol-spec-v0.2.md` — wire format (HTTP + JSON): endpoints, status codes, JSON shapes. v0.1 remains authoritative for protocol *semantics* (message meanings, state, life cycle, robustness requirements).
3. `docs/protocol-spec-v0.1.md` — tracker semantics and definition of done. (Titled *Tracker Protocol Spec — v0.1*; the filename has no `tracker-` prefix.)
4. `docs/peer-transfer-spec-v0.2.md` — the daemon↔daemon wire for package bytes (UC-02 fetch loop, UC-06 serving side): HTTP over TCP, `GET /pkg/<name-version>`, status codes, the size and hash bound, buffering and timeouts.
5. `docs/use-case-descriptions.md` — UC-01 … UC-07 behaviour spec. **UC-07 is broken as written — see ground rule 3 and `docs/logs/HANDOFF.md` §4.4.**
6. `docs/uc-*.puml` — authoritative where the prose is ambiguous.
7. `README.md` — orientation only.

The pkg↔daemon wire has **no spec file**. It is governed entirely by ADRs:
ADR-003 for fetch semantics and status codes, ADR-004 for the path rule.

These are **three separate wires** and they do not share a path grammar. The peer wire's `/pkg/<name-version>` namespace is deliberately unlike the facade's `…/All/<name-version>.pkg` so that a seeding daemon cannot be mistaken for, or used as, a pkg mirror. Do not "unify" them.

Deprecated: anything referencing IPFS, CIDs, or `peer_id`. Packages are addressed by `name-version` strings (e.g. `nginx-1.24.0_2`). If you find CID-based code or docs, flag it; do not extend it.

Also deprecated: `docs/mirror-facade-spec-v0.1.md`. It was drafted by an
implementing agent rather than the spec owner and was never binding; ADR-003
then overruled the mirror fall-through model it was built on. **There is no
v0.2 and none is planned** — the facade is governed by ADR-003 (fetch
semantics, status codes) and ADR-004 (path rule). The file is retained as
history, with a deprecation banner mapping each section to its successor. Do
not implement from it, and do not cite it as a contract. The single caveat is
in that banner: until ADR-004 is approved, its *Request surface* section is
still the only specification of a path rule that shipped code depends on.

Also deprecated: `internal/peerwire`, the interim length-prefixed binary framing for peer transfers, and its `MaxPayload` constant. It was explicitly a placeholder until the peer wire spec landed. That spec is now `docs/peer-transfer-spec-v0.2.md` and it chooses HTTP, so the package is to be deleted, not extended or chunked.

## Layout

```
cmd/trac/           tracker entry point (thin main only)
cmd/jmj/            daemon entry point (thin main only)
internal/tracker/   tracker logic
internal/proto/     message types, encoding, validation (shared by both sides)
internal/discovery/ daemon-side tracker client (announce/ping/IWant)
internal/daemon/    daemon logic — facade, fetch loop, cache watcher (created as work lands)
docs/               specs — agents do not modify these
docs/logs/          agent work logs — see below`
```

Anything not listed above is not fixed. Do not create new top-level directories
without asking.

Module path: `github.com/ndrew222/p2p-pkg-daemon`, Go 1.26.

## Work logs — required

For every feature you work on, create `docs/logs/<author>-<feature>.md`
(e.g. `docs/logs/elroy-tracker-expiry.md`). It must contain:

- Your thought process: how you chose to tackle the feature.
- Difficulties you hit and how you resolved them.
- **Areas of uncertainty**, and for each one: whether you attempted to clarify it, with whom, and the outcome. An uncertainty you silently resolved yourself is a spec violation — see ground rule 2.

## Build, test, verify

```
go build ./...
go vet ./...
go test ./...
```

No linter is configured; `gofmt` your code (this is not optional in Go anyway).
No CI exists yet — the commands above are the gate. Tracker code and tests must run on any OS: no FreeBSD dependency, no `pkg`, no second machine.
The "Definition of done" section in the tracker spec is the tracker's test list; write those as table-driven tests.

Commit and branch naming: no convention, use your judgment.

## Hard constraints (do not violate, do not "improve")

- `pkg` is never modified, wrapped, or patched. Integration surface = mirror HTTP.
- The tracker never relays package bytes and never verifies content.
- The daemon writes only to its own temp buffer directory.
  The pkg cache and repository database are read-only.
- jmj requires no write privileges to be configured. `-generate-config` prints
  a config to stdout with no side effects and the user redirects it; there is
  no config writer in the codebase and no permission handling. Do not add one.
  (The sole exception: the daemon makes a best-effort move of a corrupt config
  to `.bak` at startup, and carries on with defaults if that fails.)
- Announce lists are always full replacements, never deltas.
- No hashing at announce time; sanity checks only. The downloader verifies.
- **No fixed limit on package size.** The transfer bound is the exact expected
  size from pkg's repository database, which is stricter than any constant and
  has no ceiling. Do not reintroduce a global cap; the one that existed blocked
  1.3% of the repository outright, including llvm, rust and chromium.
- Neither end of a peer transfer holds a package in memory. The requester
  streams to a temporary file and hashes incrementally; the seeder serves from
  an open file handle. A `[]byte` in either signature is a regression.
- Peer blacklisting is local-only; nothing is reported to the tracker.
- No download throttling, no bandwidth management, no NAT traversal (ADR-001). These are out of scope. "No additional features, just implement the use cases."
  Read that correctly. Rate control is not wrong in principle and this is not a
  ban on thinking about it. It is out of scope because the earlier attempt at it
  solved a problem nobody had, and did not even solve *that* coherently. If you
  ever have a real, observed problem that rate control is the right answer to,
  say so and make the case — do not smuggle one in, and equally do not cite this
  line as a reason to leave a genuine defect unfixed.
- A slow peer is out of scope, exactly as a slow mirror is. Do not add stall
  detectors, minimum-throughput rules or transfer deadlines to "fix" it.
```
