# Handoff — instructions for the next agent

You are picking up a Go FreeBSD P2P package daemon mid-flight. This document
says what is decided, what is blocked, what is stale, and what to do next.

It is **not a spec**, and it is deliberately thin where a real document exists.
Most of what used to live here has moved into ADRs and work logs; this file now
points at them rather than restating them. Where it disagrees with a document
in `docs/`, that document wins.

Section numbers (`§4.1`, `§5.3`, `§7.1`, …) are cited from ADRs, work logs and
commit messages. **Do not renumber them.**

Keep this file current. If you resolve something here, edit it in the same
commit as the work.

## 0. Rules that actually bite

- **Read `AGENTS.md` first.** Ground rules 2 and 3 are real and enforced: if a
  spec is silent or contradictory on something you need, **stop and ask**. Do
  not pick a reasonable interpretation and continue. Ground rule 4 is new — end
  your session by stating what is waiting on the owner.
- **ADRs now outrank every spec.** `AGENTS.md` precedence entry 1 is
  `docs/adr/adr*.md`. Where an ADR and a use case disagree the ADR wins, and the
  use case is a bug to be fixed. This is load-bearing right now — see §1.
- **Atomic commits.** The owner reviews one commit at a time and has asked for
  this explicitly. One reviewable change per commit, with a message explaining
  *why*, not just what.
- **graphify.** `graphify query "<question>"` before reading or grepping source;
  `graphify update .` after changing code. See `CLAUDE.md`. **Caveat:** neither
  the binary nor `CLAUDE.md` is present in every environment, and the graph is
  stale for some commits. If you have the tool, re-run it.
- **Gate:** `go build ./... && go vet ./... && go test ./...`, plus `gofmt`.
  Tracker code and tests must run on any OS — no FreeBSD, no `pkg`, no second
  machine.
- **Work log required.** `docs/logs/<author>-<feature>.md` for every feature,
  including your areas of uncertainty and whether you raised them.

Current branch: `main`; everything through `bc5fabf` is merged.

ADR-001, -002 and -003 are all Approved, and **§5.3 is the next work item and is
fully unblocked.**

**One thing is blocked on an owner decision: whether the facade proxies pkg's
metadata.** It surfaced while bringing the specs into line with ADR-003 and it
is not a detail — UC-07 is built end to end on a fall-through that does not
exist, so as written it cannot be implemented at all. See §4.4.

## 1. Document map — what to trust

### Authoritative

| Document | Status |
|---|---|
| `AGENTS.md` | Current. Constraints and precedence. ADRs are rank 1. |
| `docs/adr/adr-001-transport-nat.md` | **Approved.** No NAT traversal; plain HTTP over TCP to the advertised IP:port. |
| `docs/adr/adr-002-serving-side-concurrency.md` | **Approved.** Global *and* per-remote-IP semaphores, `503` when either is full, default `0` = unlimited. Implement with §5.3. |
| `docs/adr/adr-003-facade-fetch-semantics.md` | **Approved.** Facade proxies to upstream on a peer miss; peer path spools, upstream path streams; no facade cache. |
| `docs/tracker-protocol-spec-v0.2.md` | Current **and implemented**. daemon↔tracker. |
| `docs/peer-transfer-spec-v0.2.md` | Current, **not implemented**. Your main work item; carries its own migration table and definition of done. Now includes ADR-002's `503`. |
| `docs/uc-05.puml`, `docs/keepalive.md` | Current and implemented. |
| `docs/uc-01.puml`, `cmd/jmj/README.md` | Current as of the two-address config and `repo_db_dir`. |
| `docs/uc-06.puml` | Current as of the HTTP peer wire. |

### Brought into line with the ADRs — **done, no longer a trap**

Four documents encoded the fall-through model that §7.1 measured false. They
have now been corrected in place; the old text is struck through rather than
deleted, so the reasoning that changed stays visible.

| Document | What changed |
|---|---|
| `docs/use-case-descriptions.md` UC-02 | Description, precondition, error-state list and the assumptions paragraph now carry the ADR-003 model. New alternative flow **8f–10f (upstream fallback)** and error state **9g–11g (upstream also failed → terminal `502`)**. Flows 6a/7a, 7b/8b and 9d/10d no longer end in "pkg tries its next mirror" — they route to 8f. |
| `docs/mirror-facade-spec-v0.1.md` | Supersession banner at the top; *What the facade is* rewritten around proxy-with-fallback and the asymmetric verification placement; status-code table rebuilt (see below); *What the facade does not do* now covers the no-facade-cache ruling. |
| `docs/uc-02.puml` | New `Upstream Mirror` participant and an `opt no verified bytes from any peer` block carrying both outcomes. Renders clean (`plantuml -checkonly`). |
| `docs/peer-transfer-spec-v0.2.md` | ADR-002's `503`: response-table row, a *Serving side obligations* bullet, the "`503` must not re-announce" rule next to the `404` obligation, and the concurrency row in *Deliberately unspecified* closed. |

**The facade status table is the change most worth reading before touching the
facade.** Four separate conditions the old table answered — tracker unreachable,
empty peer list, all holders blacklisted, all holders tried and failed —
collapsed into one row, because under ADR-003 all four go to upstream and none
is visible to pkg. `404` narrowed to "provably absent from the repository
database"; `502` narrowed to "peers *and* upstream both failed". An empty peer
list is **no longer an error at all** — it is the common case, and answering
`404` to it would have failed every first-of-its-kind install.

Also closed, from the §7.3 measurement: facade open questions **1 (`HEAD`)** and
**5 (`Range`)**. pkg 2.7.5 issues neither — every request across a catalogue
refresh, a `pkg fetch` and a real `pkg install` was a plain `GET`. Both stay as
they are. The `Range` caveat is recorded in the spec: no observed transfer was
ever interrupted, so resume-after-interrupt is untested and is the one place a
`Range` request would plausibly appear.

### Historical

| Document | Status |
|---|---|
| `docs/protocol-spec-v0.1.md` | Still authoritative for tracker **semantics**; its wire encoding is superseded by v0.2. |
| `docs/logs/elroy-uc1-config.md` §"Decision 3" | **Actively misleading.** Justifies `buffer_dir` as persisting across reboots — wrong; the daemon has no store and the buffer is per-request. The field no longer exists. Another author's work log; read as history only. |

## 2. State of the tree

Gate passes. What exists and works:

- `cmd/trac` — tracker, spec-complete against v0.2.
- `internal/discovery` — tracker client + keep-alive loop, wired into the daemon.
- `internal/daemon/watcher.go` — cache watcher, wired, read-only. Skips symlinks
  and strips `~hash10` (§4.1).
- `internal/config` — load / validate / generate-to-stdout. Two listen addresses,
  `facade_addr` loopback-enforced, plus `repo_db_dir`. No writer, by design.
- `internal/daemon/repodb.go` — `Repositories`, the repository database reader
  (§5.2). Read-only in-memory snapshot of every catalogue under `repo_db_dir`.
- `internal/daemon/facade.go` — the mirror facade handler, **mounted on
  `facade_addr`**. Spools through `temp_dir`; skips and marks blacklisted peers
  via `peer.FetchFirst`.
- `internal/peer` + `internal/peerwire` — fetch and seed over the interim binary
  framing. **Not mounted**, and being replaced (§5.3).
- `internal/peer/blacklist.go` — the local peer blacklist (§5.5). In-memory, no
  expiry, not persisted.

**What does not exist at all: the seed half.** There is no production
`PackageSource` implementation anywhere in the tree — the only implementors are
two test fakes and `cmd/demo`'s in-memory store, and nothing reads the pkg cache
for file *contents* (the watcher reads it for names only). `peer.Server` is
constructed in exactly one place, `cmd/demo/main.go`.

So **§5.3 is a build, not just a migration.** Budget for writing a cache-backed
seeder from scratch.

**The facade model has now been exercised against real pkg and works** — see §7.
A stand-in that proxied the signed catalogue was accepted as a genuine
repository (37,789 packages, `signature_type: fingerprints` intact) and a real
`pkg install` through it succeeded end to end. That is the first time any of
this ran against real pkg.

## 3. Decided — implement, do not re-litigate

All settled and documented elsewhere. Read the linked document, not a summary.

| Topic | Where | Outcome in one line |
|---|---|---|
| Transport / NAT | ADR-001 | Punt. Plain HTTP/TCP; every daemon can fetch, only reachable ones seed. |
| Serving concurrency | ADR-002 | Global **and** per-remote-IP semaphores, `503`, default `0` = unlimited. |
| Facade fetch semantics | ADR-003 | Proxy to upstream on a peer miss. Peer path spools, upstream path streams. No facade cache. |
| Config schema | `docs/logs/claude-config-schema.md` | Two addresses, `temp_dir`, no config writer. |
| Repository DB reader | `docs/logs/claude-repo-db-reader.md` | A directory not a file; `modernc.org/sqlite`; snapshot not live query. |
| Peer blacklist | `docs/logs/claude-peer-blacklist.md` | Local only, no expiry, not persisted, whole-peer not per-package. |
| Cache/path layout (§4.1) | `docs/logs/claude-verification-rulings.md` | Locate `All` anywhere, optional `Hashed/`, strip `~[0-9a-f]{10}`. |
| Verification rulings (§4.3, §5.5) | `docs/logs/claude-verification-rulings.md` | First-wins on collisions; malformed rows dropped; only a hash mismatch blacklists. |
| Peer transfer wire | `docs/peer-transfer-spec-v0.2.md` | HTTP over TCP, `/pkg/<name-version>`, fuzz target. |

## 4. Blocked on the owner

§4.1, §4.2 and §4.3 were the standing blockers. All three are resolved — §4.2 by
ADR-002, the other two by the verification rulings. The numbers are retained
because other documents cite them — §4.1 from `claude-config-schema.md` and
`claude-verification-rulings.md`, §4.2 from **ADR-002 twice**, and §4.3 from
`internal/daemon/facade.go:59` and `internal/daemon/repository.go:19`. **Do not
reuse §4.1–§4.3 for new items.** New blockers take fresh numbers from §4.4.

### 4.4 Does the facade proxy pkg's metadata? — **NEEDS AN OWNER RULING**

**"Metadata" here means the repository catalogue files, not any hash.** Concretely
the non-package paths in the facade's own list: `meta.conf`, `packagesite.pkg`,
`data.pkg`, directory listings and `/`. These are whole-repository documents that
pkg downloads during `pkg update` — `packagesite.pkg` is the signed catalogue
carrying all 37,789 package records, and it is the root of the integrity model.

It is **not** the `~hash10` suffix or `packages.cksum`. Those are per-package
identifiers on the package-file path (`All/Hashed/foo-1.0~ae9dce33aa.pkg`, and
the 64-char lowercase hex SHA-256 in the repository database row), they belong to
the *ratified* path rule, and nothing in ADR-003 touches them. The question below
is about a different class of request entirely: what the facade does when pkg
asks it for the catalogue rather than for a package.

Two ratified statements now contradict each other and no ADR settles it.

- `mirror-facade-spec-v0.1.md` and UC-07: *"The daemon never serves, caches or
  proxies metadata"*, because the signed catalog is the root of the integrity
  model and must come from a real mirror.
- ADR-003 makes jmj pkg's **only** mirror. §7.1 measured that a facade which
  errors on a metadata path breaks `pkg update` outright — there is no second
  repository to fall through to. ADR-003 also expects the facade to forward a
  conditional `GET` and relay upstream's `304`, which *is* metadata proxying,
  and the §7 harness proxied the signed catalogue successfully (37,789
  packages, `signature_type: fingerprints` intact).

So the design as it stands cannot run: refuse metadata and `pkg update` fails;
proxy it and a ratified sentence is violated. **Do not resolve this by reading
ADR-003 generously** — its *Decision* section rules on package files only, and
ground rule 2 makes this an owner call.

Worth noting for whoever rules: relaying is not the same as vouching. The bytes
still originate at the real mirror and pkg still verifies the repository
signature itself, so *"the catalog comes from a real mirror"* can survive a
pass-through. It is only *"never proxies metadata"* that cannot.

**What it blocks:** all of UC-07, and any end-to-end use of the daemon, since
`pkg update` precedes every install. It does **not** block §5.3, which is peer
wire only. **What unblocks it:** one ADR. **Cost of leaving it:** the specs stay
mutually contradictory, and the facade cannot be finished — §5.7 stops at the
package path.

Flagged in place at `docs/mirror-facade-spec-v0.1.md` (open question 7 and a
warning under *Request surface*) and at UC-07's description, so nobody
implements either reading by accident.

### 4.5 Which upstream mirror, and what the config key is called

ADR-003 requires a configured upstream and deliberately leaves the key name, the
TLS decision and the choice of mirror open. Note that
`pkg+https://pkg.FreeBSD.org/${ABI}/quarterly` resolves via DNS SRV, so the
daemon must either resolve SRV itself or be pointed at a concrete host such as
`pkgmir.geo.freebsd.org`. **Blocks §5.7.** Do not invent a key name — `AGENTS.md`
ground rule 3.

## 5. Work, in order

### 5.1 Config schema — **DONE**

### 5.2 Repository database reader — **DONE**

One follow-up remains: **nothing triggers `Reload()`**. `pkg update` rewrites the
catalogues, so a long-running daemon goes stale and starts answering `404` for
packages added since startup. `Reload()` exists and is tested; wiring a trigger
is unclaimed. Choosing between a watch on `repo_db_dir` and a periodic reload is
a design decision in no spec — **ask before picking**.

### 5.3 Peer wire migration — **NEXT, fully unblocked**

Work to `docs/peer-transfer-spec-v0.2.md`'s migration table and definition of
done. Deletes `internal/peerwire`; rewrites `peer.Server`, `FetchFromPeer`,
`PackageSource`, `cmd/demo` and both peer test files; **and builds the
cache-backed seeder that has never existed** (§2).

Three things it must carry that are easy to lose:

- **Both ADR-002 semaphores** — global and per-remote-IP — replying `503`.
  Remote identity is the host half of `r.RemoteAddr` via `net.SplitHostPort`,
  never a header. `503` must not trigger the UC-06 §5b re-announce, and must not
  carry `Retry-After`.
- **The size bound as real code**: `io.LimitReader(body, expectedSize+1)` plus
  the `Content-Length` check.
- **Constant memory on both ends.** A `[]byte` in either signature is a
  regression, and is what currently OOMs a 1 GiB host on the 2.83 GiB package.

**Do not delete `internal/peerwire` before that size bound is in place.**
`MaxPayload` (`wire.go:24`, enforced at `:53`) is today the *only* length check
on the fetch path, so removing the package first leaves the fetch loop with
nothing between it and a hostile peer streaming unbounded bytes. This is
ordering within §5.3, not a reprieve — `MaxPayload` is a global cap of exactly
the kind §8 forbids, and it goes when its replacement lands, in the same change.

### 5.4 Mount the facade and the seed server — **facade half DONE**

The seed-server half belongs to §5.3. Trap for whoever touches this: a nil
`*Repositories` assigned into an interface field is a **non-nil interface
holding a nil pointer**, so every `== nil` check downstream passes and the first
call panics. Both wiring sites go through `Daemon.repository()` for this reason;
`TestStartHTTPServerRefusesWithoutARepositoryDatabase` is the regression test.

### 5.5 Local peer blacklist — **DONE**

### 5.6 §4.1 cache-layout cross-check — **CLOSED** by §7.5.

### 5.7 Facade rework under ADR-003 — **unclaimed, needs scoping**

ADR-003 is approved and nothing is implemented. This is the second-largest piece
of work in the tree after §5.3 and is largely independent of it: the new
upstream-mirror config key, streaming the upstream path, and the three document
corrections listed in §1.

## 6. Known defects

- `cmd/demo` depends on `peerwire` and on `PackageSource.Get` returning
  `[]byte`. It must be rewritten in §5.3, not deleted.
- **The daemon announces a serving port nothing listens on.** `daemon.go`
  announces `config.ServingPort()` and the keep-alive announces the whole cache,
  but no seed server is mounted and none can be until §5.3. Every peer acting on
  our tracker entry dials `serving_addr` and gets connection-refused. That
  correctly does not blacklist us — a dial failure never does — so the cost is
  one wasted attempt per peer, paid by the rest of the swarm. Worth knowing
  before reading a trial's peer logs and concluding the tracker is broken.
- **The facade has no answer for `If-Modified-Since`.** pkg sends conditional
  `GET`s for catalogue files. Ignoring the header wastes catalogue bandwidth on
  every `pkg update`; answering `304` from a guess would serve a stale
  catalogue. ADR-003's proxying resolves this for free, but is unimplemented.

## 7. Empirical findings — §7.1–§7.5 are ANSWERED

Full evidence, controls and a copy-pasteable demo:
**`docs/logs/claude-pkg-mirror-verification.md`**. Read it before any facade or
UC-02 work. Summary only:

1. **Does pkg fall through on a non-200?** *Yes between mirrors of one
   repository; **no** between repositories.* `404`, `503` and connection-refused
   all fail the install with exit 1 while a healthy repository holding the
   package sits alongside. **This is the finding that produced ADR-003.**
2. **How is mirror ordering configured?** None of the three mechanisms delivers
   daemon-first today. `mirror_type: srv` needs DNS control; two repositories
   with `priority` give selection without retry; `mirror_type: http` fits
   exactly and **segfaults pkg 2.7.5**.
3. **`HEAD` or `Range`?** Neither — every request is a plain `GET`. But pkg does
   send `If-Modified-Since` on catalogue files; see §6.
4. **200 with a body failing pkg's checksum?** Caught as a checksum failure, and
   likewise with nowhere to fall back to.
5. **Cache layout after a real install?** Matches the earlier `pkg fetch -o`
   probe; the cache stays flat. Closes §5.6.

Remaining unknowns, both minor: whether `cksum` is ever not sha256-hex (38,074
rows checked across both repositories, zero exceptions; residual risk is one
*host*, not one repository), and where the tracker runs for a real two-machine
trial.

**Host access:** the owner has an SSH-accessible FreeBSD 15.1-RELEASE-p1 /
pkg 2.7.5 box and holds the address. *(Deliberately not recorded here — this
repository is public. Ask the owner.)* The host was returned to a verified clean
baseline after the §7 run; a `pkg.core` was left in place as evidence.

**A draft FreeBSD bug report exists** for the `mirror_type: http` segfault, not
yet filed. Before filing it needs: the **child** process's core — the parent's
core is only a signal re-raise and has no diagnostic value — captured via
`sysctl kern.corefile='%N.%P.core'`; and two isolation runs, one with
`signature_type: none` and one serving the mirror list from a stock web server
rather than a purpose-built one.

## 8. Traps

Things previous agents got wrong, or were explicitly told not to do.

- **Do not reintroduce a global package size cap.** The one that existed blocked
  1.30% of the repository outright, including llvm, rust, chromium and
  libreoffice. The bound is the exact expected size from the repo DB.
- **Do not add stall detectors, minimum-throughput rules or transfer deadlines.**
  A slow peer is out of scope exactly as a slow mirror is.
- **Do not unify the three path grammars.** Tracker, facade and peer wire are
  separate surfaces, and the peer namespace differs from the facade's on purpose.
- **Do not add a config writer**, or any permission handling to
  `-generate-config`. It prints to stdout and the shell does the writing.
- **Do not create directories in, or write to, the pkg cache or the repository
  database.** The watcher used to `MkdirAll` the cache dir; that was a hard
  constraint violation and is fixed. Do not reinstate it.
- **Do not read the existing config as a merge base in `-generate-config`.** The
  shell truncates the redirect target before jmj starts, so there is nothing to
  merge.
- **PlantUML:** square brackets in an `alt`/`else` label are parsed as a link and
  eat the first word; angle brackets in a note body are parsed as markup. Render
  to PNG and *look at it* before committing a diagram.
- **Do not claim a fact about a system you have not inspected.** An earlier
  session asserted a "flat GhostBSD-style cache layout" that was an invention;
  the owner caught it.

Added 2026-08-08, from the §7 work:

- **Do not file a settled constraint as an open question.** ADR-003's draft
  listed facade-side caching as "not decided here" when three documents already
  forbid a daemon-owned store. Manufacturing a gap invites the next agent to
  re-litigate it — the mirror image of inventing an answer, and just as costly.
- **A plausible self-attributed cause is not a control.** The pkg segfault was
  confidently blamed on a bug in our own harness. The bug was real; it was not
  the cause, and only a control that removed our code from the path settled it.
  "Our tooling is probably at fault" needs evidence exactly as much as "the
  system under test is at fault" does.
- **`which pkg` returns `/usr/sbin/pkg`**, the base-system bootstrap stub.
  Symbolising a core against it yields only unnamed symbols. The real binary is
  `/usr/local/sbin/pkg`, and libpkg is statically linked into it.
- **A `repos/*.conf` block shadows by name.** `/usr/local/etc/pkg/repos/` is read
  *after* `/etc/pkg/FreeBSD.conf`, so a block named `FreeBSD-ports` **replaces**
  the stock definition rather than adding to it. Get it wrong and the host has no
  working package manager until the file is removed.
- **Verify teardown and "it's clean now" claims against the host yourself.** It
  is three read-only commands, and a report is not evidence.

## Suggested skills

- **`graphify`** — mandated by `CLAUDE.md`. Run `graphify query "<question>"`
  before reading or grepping source, and `graphify update .` after changing
  code. Start here for orientation rather than browsing `internal/` by hand.
- **`/code-review`** — §5.3 is the largest code change in the project's history:
  it deletes a package, rewrites four files and two test files, and builds a
  seeder from nothing. Review before requesting a merge.
- **`security-review`** — appropriate for §5.3 specifically. It implements
  admission control against an explicitly hostile peer model, and the size bound
  is the only thing standing between the fetch loop and unbounded bytes.
- **`run`** — for exercising the daemon end to end once a seed server exists.

Do **not** reach for a planning or architecture skill on §5.3. The design is
already settled across ADR-002, `peer-transfer-spec-v0.2.md` and its migration
table; re-planning it would re-litigate decided questions and violate ground
rule 1.
