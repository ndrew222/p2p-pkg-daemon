# AGENTS.md

Instructions for AI coding agents working in this repository. Human contributors:
read the README and `docs/` instead; this file assumes you are an agent.

## Ground rules

1. **Spec first, always.** The documents in `docs/` are the contract. Every change must map to a use-case step, the tracker protocol spec, or an ADR. If no spec covers what you are about to write, STOP — do not write the code.
2. **Ambiguities are live wires.** If a spec is unclear, contradictory, or silent on something your task needs, STOP AND WAIT. Report the ambiguity to the user you are working with, so they can escalate it to the spec owner. Do not pick a "reasonable" interpretation and continue. Do not code around it.
3. **Deliberately unspecified means ask, not invent.** The specs mark some items as open decisions (see the table at the bottom of `docs/tracker-protocol-spec-v0.1.md`). These are not gaps for you to fill.

## Authoritative documents, in precedence order

1. `docs/tracker-protocol-spec-v0.2.md` — wire format (HTTP + JSON): endpoints, status codes, JSON shapes. **If this file does not exist yet, all wire-level code is blocked. Stop and wait.** v0.1 remains authoritative for protocol *semantics* (message meanings, state, life cycle, robustness requirements).
2. `docs/tracker-protocol-spec-v0.1.md` — tracker semantics and definition of done.
3. `docs/use-case-descriptions.md` — UC-01 … UC-07 behaviour spec.
4. `docs/diagrams/uc-*.puml` — authoritative where the prose is ambiguous.
5. `README.md` — orientation only.

Deprecated: anything referencing IPFS, CIDs, or `peer_id`. Packages are addressed by `name-version` strings (e.g. `nginx-1.24.0_2`). If you find CID-based code or docs, flag it; do not extend it.

## Layout

```
cmd/trac/        tracker entry point (thin main only)
cmd/jmj/         daemon entry point (thin main only)
internal/tracker/  tracker logic
internal/daemon/   daemon logic (facade, fetch loop, cache watcher, keep-alive)
docs/            specs — agents do not modify these
docs/logs/       agent work logs — see below
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
- The daemon writes only to its own temp buffer directory and config path.
  The pkg cache and repository database are read-only.
- Announce lists are always full replacements, never deltas.
- No hashing at announce time; sanity checks only. The downloader verifies.
- Peer blacklisting is local-only; nothing is reported to the tracker.
- No download throttling, no bandwidth management, no NAT traversal (ADR-001). These are out of scope. "No additional features, just implement the use cases."
```
