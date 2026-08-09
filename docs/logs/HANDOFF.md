# Handoff — instructions for the next agent

You are picking up a Go FreeBSD P2P package daemon mid-flight. This document
says what is decided, what is blocked, what is stale, and what to do next.

It is **not a spec**, and it is deliberately thin where a real document exists.
Most of what used to live here has moved into ADRs and work logs; this file now
points at them rather than restating them. Where it disagrees with a document
in `docs/`, that document wins.

Section numbers (`§4.1`, `§5.3`, `§7.1`, …) are cited from ADRs, work logs,
commit messages and **Go source comments**. **Do not renumber them, and do not
recycle a retired number for a new item.**

### Where the cited-but-retired numbers went

The rewrite at `234a75b` folded several subsections into tables and topic
pointers, so some numbers other documents cite no longer have a heading here.
They are not dead — this is where each resolves.

| Cited as | Cited by | Where it lives now |
|---|---|---|
| §3.1 | **ADR-003** (twice: the `temp_dir` consumer, and the `FetchFromPeer` `[]byte` blocker) | Config schema, done — §5.1. The `[]byte` blocker is §5.3; the `temp_dir` concern is resolved by ADR-003 itself, which narrows the justification to "retry needs the whole file". |
| §4.1 | `claude-config-schema.md`, `claude-verification-rulings.md` | Cache/path layout ruling — §3's topic table → `claude-verification-rulings.md`. Cross-check closed by §7.5, recorded at §5.6. |
| §4.2 | **ADR-002** (twice) | Serving-side concurrency. Superseded outright by ADR-002; §5.3 implements it. |
| §4.3 | `internal/daemon/facade.go:59`, `internal/daemon/repository.go:19` | Repository-database rulings — §3's topic table → `claude-verification-rulings.md`. |
| §7.1–§7.5 | ADR-003, `claude-pkg-mirror-verification.md` | All answered — §7, full detail in `claude-pkg-mirror-verification.md`. |
| §7.6 | `claude-config-schema.md`, `claude-repo-db-reader.md` | **Never had a definition anywhere**, including before the rewrite. From its two citations it was the residual risk that `cksum` format was measured on one repository and one ABI. `claude-repo-db-reader.md` reports 0 of 38,074 rows non-conforming across *both* repositories, which substantially retires it. Treat the number as historical; do not reuse it. |

New blockers take fresh numbers from **§4.4** onward.

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

ADR-001 through -007 are all Approved, and **§5.3 is the next work item and is
fully unblocked.**

**§4.4, §4.5 and §4.6 are all closed** (ADR-005, ADR-006 and ADR-007, ruled
2026-08-08). **No work is blocked and no question is open for the owner.** §5.7
— the facade rework — is unblocked for the first time, and is now the largest
open item alongside §5.3.

Read before starting §5.7: ADR-003 (fetch semantics), ADR-004 (path rule),
ADR-005 (metadata is proxied), ADR-006 (`upstream_url`) and ADR-007 (jmj fronts
one repository and coexists with the rest). The key exists, validates and
expands; nothing consumes it yet. ADR-007 carries one trap into the rework: a
successful repository-database lookup is **not** proof the upstream can serve
that package.

## 1. Document map — what to trust

### Authoritative

| Document | Status |
|---|---|
| `AGENTS.md` | Current. Constraints and precedence. ADRs are rank 1. |
| `docs/adr/adr-001-transport-nat.md` | **Approved.** No NAT traversal; plain HTTP over TCP to the advertised IP:port. |
| `docs/adr/adr-002-serving-side-concurrency.md` | **Approved.** Global *and* per-remote-IP semaphores, `503` when either is full, default `0` = unlimited. Implement with §5.3. |
| `docs/adr/adr-003-facade-fetch-semantics.md` | **Approved.** Facade proxies to upstream on a peer miss; peer path spools, upstream path streams; no facade cache. |
| `docs/adr/adr-004-facade-path-rule.md` | **Approved.** Carries the `All/` + `Hashed/` + `~hash10` path rule out of the deprecated facade spec. Introduces no new decision; if it differs from that spec's text, the spec wins. |
| `docs/adr/adr-005-metadata-proxying.md` | **Approved.** The facade proxies non-package paths to the configured upstream and relays the response, including `304`. Closes §4.4; retires *"never proxies metadata"*. |
| `docs/adr/adr-006-upstream-mirror-config.md` | **Approved and implemented** (the key, not its consumer). `upstream_url` in jmj's config: required, no default, `${ABI}` expanded at startup, plaintext warned not refused. Closes §4.5. |
| `docs/adr/adr-007-repository-topology.md` | **Approved.** jmj fronts one repository, replaces that one, coexists with every other enabled repository. `upstream_url` stays singular. Closes §4.6; corrects a misreading of ADR-003 in §4.6 and ADR-006. |
| `docs/tracker-protocol-spec-v0.2.md` | Current **and implemented**. daemon↔tracker. |
| `docs/peer-transfer-spec-v0.2.md` | Current, **not implemented**. Your main work item; carries its own migration table and definition of done. Now includes ADR-002's `503`. |
| `docs/uc-05.puml`, `docs/keepalive.md` | Current and implemented. |
| `docs/uc-07.puml` | Current, **not implemented**. New — UC-07 had no diagram before ADR-005. Carries the relay flow, the 304 branch and the terminal `502`. |
| `docs/uc-01.puml`, `cmd/jmj/README.md` | Current as of the two-address config and `repo_db_dir`. |
| `docs/uc-06.puml` | Current as of the HTTP peer wire. |

### Brought into line with the ADRs — **done, no longer a trap**

Four documents encoded the fall-through model that §7.1 measured false. They
have now been corrected in place; the old text is struck through rather than
deleted, so the reasoning that changed stays visible.

| Document | What changed |
|---|---|
| `docs/use-case-descriptions.md` UC-02 | Description, precondition, error-state list and the assumptions paragraph now carry the ADR-003 model. New alternative flow **8f–10f (upstream fallback)** and error state **9g–11g (upstream also failed → terminal `502`)**. Flows 6a/7a, 7b/8b and 9d/10d no longer end in "pkg tries its next mirror" — they route to 8f. |
| `docs/mirror-facade-spec-v0.1.md` | **Now DEPRECATED outright** — see below. It was first corrected in place (proxy-with-fallback, asymmetric verification, rebuilt status table), then deprecated by the owner once ADR-003 was confirmed as its successor. Read it as history only. |
| `docs/uc-02.puml` | New `Upstream Mirror` participant and an `opt no verified bytes from any peer` block carrying both outcomes. Renders clean (`plantuml -checkonly`). |
| `docs/peer-transfer-spec-v0.2.md` | ADR-002's `503`: response-table row, a *Serving side obligations* bullet, the "`503` must not re-announce" rule next to the `404` obligation, and the concurrency row in *Deliberately unspecified* closed. |

### The pkg↔daemon wire has no spec file

`mirror-facade-spec-v0.1.md` is **deprecated** (owner ruling, 2026-08-08). It was
never binding — its own status block says it was drafted by an implementing
agent, not the spec owner — and ADR-003 overruled the model it was built on.
There is no v0.2 and none is planned. The facade is governed by **ADR-003**
(fetch semantics, status codes, verification placement, no cache) and
**ADR-004** (path rule, `GET`-only).

The file is retained, banner-first, mapping each section to its successor. The
path rule that shipped code depends on — `internal/daemon/facade.go`,
`watcher.go`, `repodb.go` and three test files — now lives in **ADR-004
(Approved)**; the deprecated spec's *Request surface* section is history, not the
governing text.

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
reuse §4.1–§4.3 for new items.** New blockers take fresh numbers from §4.8.

### 4.4 Does the facade proxy pkg's metadata? — **RULED. Yes.** (ADR-005)

**Closed 2026-08-08 by owner ruling, recorded in
`docs/adr/adr-005-metadata-proxying.md` (Approved).** The facade fetches
non-package paths from the configured upstream mirror and relays them —
streamed, uncached, unverified, unmodified, with `If-Modified-Since`/`304`
relayed unchanged. The sentence that gave way is *"the daemon never serves,
caches or proxies metadata"*; *"the signed catalog comes from a real mirror"*
survives, because relaying is not vouching and pkg verifies the signature
itself.

Propagated to UC-07 (rewritten — steps 3–5 and a new upstream-failure flow),
`AGENTS.md`, and the deprecated facade spec's four flags. **§5.7 is now blocked
on §4.5 alone.** Note two consequences worth carrying into the rework: the
facade becomes a general reverse proxy for non-package paths, so the loopback
enforcement on `facade_addr` is load-bearing and must not be relaxed; and a
proxied path is client-supplied, so joining it to the upstream base URL must
not permit escaping that base.

The original statement of the problem follows, retained because it records why
the rule changed.

---

#### Original blocker text (historical)

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

### 4.5 Which upstream mirror, and what the config key is called — **RULED** (ADR-006)

**Closed 2026-08-08 by owner ruling, recorded in
`docs/adr/adr-006-upstream-mirror-config.md` (Approved). This was the last
blocker on §5.7, which is now fully unblocked.**

The key is **`upstream_url`**, set in jmj's own config; discovery from pkg's
config is *not* the source. It is **required and has no default**, because under
ADR-005 it decides which repository pkg installs from — so `-generate-config`
refuses to emit without `-upstream`, which is how UC-01's "defaults are valid by
construction" survives a key that cannot be guessed. `${ABI}` is expanded at
**startup only**, via `pkg config abi` (ruled permissible), and only when the
placeholder is present, so a literal URL never shells out and the daemon still
runs off FreeBSD. Plaintext `http` is **warned about, not refused** — unlike
`facade_addr`, because tampering is still caught by pkg's signature check and
jmj's hash check. The branch/ABI mismatch is **advisory**.

**Implemented** in `internal/config` and `cmd/jmj` — see
`docs/logs/claude-upstream-url-key.md`. **The advisory cross-check is now
implemented too** (`internal/daemon/upstreamcheck.go`), against a better source
than this section originally anticipated: the repository database's `repodata`
table records the upstream URL already expanded, so no UCL parsing is needed.
Verified against the reference host.

**One thing is deliberately still NOT done:** nothing consumes `upstream_url`
yet, because the facade is frozen — it is validated, expanded, cross-checked,
and then unused until §5.7 lands.

The original statement of the problem follows, retained because it records the
reasoning and the measurements that fed the ruling.

---

#### Original blocker text (historical)

ADR-003 requires a configured upstream and deliberately leaves the key name, the
TLS decision and the choice of mirror open. **Blocks §5.7, and is now the only
thing that does.** Do not invent a key name — `AGENTS.md` ground rule 3.

**Tradeoff analysis: `docs/logs/claude-upstream-mirror-config.md`** (written
2026-08-08 at the owner's request; recommends without deciding). It compares an
explicit key, discovery from pkg's config, and the hybrid below, and recommends
an explicit required key plus a *best-effort advisory* cross-check against pkg's
config — so that the fragile parsing only ever powers a warning.

**Partially ruled by the owner, 2026-08-08 — four sub-decisions still open.**

Settled:

| Question | Ruling |
|---|---|
| Where does the upstream URL come from? | **jmj's own config** — option A. Discovery from pkg's config is *not* the source. |
| Is the silent branch/ABI mismatch a hard check? | **No — advisory.** Warn; do not refuse to start. |
| May the daemon execute `pkg config abi`? | **Yes, permissible.** Settles the constraint question the analysis raised; executing pkg is not "wrapping" it. |
| Must the advisory cross-check parse UCL? | **No.** The owner notes the file is greppable, and grep plus `pkg config abi` is acceptable for an advisory check. |

Still open — **these block §5.7 and must not be invented** (ground rule 3): the
**key name**; the **default value**, which is forced to exist by UC-01's
"defaults are valid by construction" and therefore forces a choice of mirror
*and* branch; whether the daemon **expands `${ABI}`** (and any other pkg URL
variables) inside the key; and whether a **plaintext upstream** is refused or
merely warned about. See the questions raised with the owner on 2026-08-08.

**ADR-005 widened what this setting is.** The facade now proxies the catalogue,
so the upstream URL no longer names a fallback source: it names **the repository
pkg actually gets**. pkg's config points the jmj repo at loopback and says
nothing about which real repository that is. A wrong upstream therefore does not
error — pkg fetches the catalogue through jmj from the wrong branch, populates
its database from it, and every hash matches, because the system is
self-consistent and both branches carry the same signature. Whatever is ruled
should say what detects that, if anything.

**Correction: SRV is not a problem, and an earlier draft of this section said it
was.** The claim was that the daemon must resolve SRV itself or be pointed at a
concrete host. Measured, 2026-08-08:

```
pkg.FreeBSD.org.            CNAME  pkgmir.geo.FreeBSD.org.
                            A      151.101.{1,65,129,193}.241     (Fastly)
                            AAAA   2a04:4e42:{::,200::,400::,600::}497
_https._tcp.pkg.FreeBSD.org SRV    10 10 443 pkgmir.geo.freebsd.org.
```

`pkg.FreeBSD.org` resolves through ordinary A/AAAA records, so Go's stdlib HTTP
client reaches it with no special handling. And the SRV record holds **one
target, on port 443, naming the host the CNAME already resolves to** — so SRV
buys nothing today that plain DNS does not. Let DNS handle it.

The honest residual: FreeBSD *could* later publish several prioritised SRV
targets, and pkg would honour them while jmj would not. That is a divergence in
mirror selection, not a failure — plain DNS still yields a working host — and it
is not a reason to hand-roll a resolver now.

### Candidate: discover the upstream from pkg's own config instead of a new key

Worth considering before adding a config key, because it gives a zero-config
happy path. The URL already exists on the machine, in `/etc/pkg/FreeBSD.conf`:

```
FreeBSD: { url: "pkg+https://pkg.FreeBSD.org/${ABI}/quarterly", ... }
```

> **Correction, measured 2026-08-08.** The block is named **`FreeBSD-ports`**,
> not `FreeBSD`, and the file holds **three** blocks — `FreeBSD-ports` and
> `FreeBSD-ports-kmods` enabled, `FreeBSD-base` disabled. The kmods and base
> URLs also carry `${VERSION_MINOR}`. The file's own header comment tells
> operators to disable a repository with a shadow file setting `enabled: no`
> rather than by editing or deleting this one, so the documented disable path
> **preserves** the URL. Superseded in practice: ADR-006 takes the comparison
> URL from the repository database's `repodata` table instead, already
> expanded, so none of this needs parsing.

Reading it is consistent with the constraints — it is read-only, and the daemon
already reads pkg's repository database. Three wrinkles the ruling should
address:

1. **`${ABI}` is unexpanded in the file.** The daemon must substitute it
   (`pkg config abi` reports it, e.g. `FreeBSD:15:amd64`).
2. **The `pkg+` prefix must be stripped** to get a URL an ordinary HTTP client
   accepts.
3. **Discovery can come up empty.** Under ADR-003 jmj must be the only
   *enabled* repository, so the stock block has to be disabled — `enabled: no`
   leaves it readable and discovery still works, but an operator who deletes the
   block or renames the repo leaves nothing to find. Whatever is decided needs a
   defined behaviour there; silently proxying from nowhere is the bad outcome.

A reasonable shape, if the owner wants one: an optional explicit key that
overrides, defaulting to discovery, and a hard startup failure when neither
yields a URL. **Not decided — recorded as a candidate, not a design.**

**The analysis argues against exactly that shape** (`claude-upstream-mirror-config.md`,
option C): it pays the whole cost of discovery — a UCL parser, pkg's multi-file
shadowing semantics, `${ABI}` expansion, none of it testable in the gate — to
make optional a path an explicit key already covers, and it leaves "why is jmj
proxying from *there*?" with two possible answers instead of one. Also recorded
there: a fourth wrinkle beyond the three above — deleting the stock block, which
is the natural way to disable a repository, leaves discovery nothing to find,
so discovery couples jmj to *how* the operator disables the stock repo.

### 4.6 jmj has one upstream; a stock host has several repositories — **CLOSED (ADR-007)**

Raised 2026-08-08 while implementing ADR-006's cross-check; ruled the same day.
Open for one working session. **It rested on an error, which was mine.**

The question as raised assumed *"ADR-003 requires jmj to be pkg's only enabled
repository"*, and concluded that adopting jmj silently drops the others.
**ADR-003 says no such thing.** It says jmj becomes pkg's only *mirror* rather
than its first — and mirrors-versus-repositories is precisely the distinction
ADR-003's Context section exists to draw. Collapsing the two words invented a
constraint and then spent an open question on it.

**ADR-007 rules: jmj fronts one repository, replaces that one, and coexists with
the rest.** Other enabled repositories are left exactly as they are and continue
to fetch directly. `upstream_url` stays a single URL, which is now the accurate
expression of the model rather than a limitation of it.

Coexistence needs no cross-repository fall-through, which is the mechanism
ADR-003 measured as absent. Because the facade proxies upstream on a peer miss,
jmj never `404`s for anything in its own catalogue, so there is no failure for a
second repository to absorb. What multi-repository operation actually uses is
**solve-time selection**, which ADR-003 affirms in the same breath as it denies
retry.

Two measurements worth keeping (full set in
`docs/logs/claude-multi-repository.md`):

- **`FreeBSD-ports` and `FreeBSD-ports-kmods` share 238 of 239 package names**,
  differing only in version. The intuition that they are disjoint is false.
  Exactly one package is kmods-exclusive: `drm-latest-kmod`.
- **They share zero name-*versions*.** That is the figure that matters, because
  name-version is what ADR-004's path rule keys on — so jmj holding both
  catalogues under `repo_db_dir` creates no ambiguity in the identifier the
  facade actually uses.

**Carried into §5.7:** jmj's repository-database view is *broader* than its
upstream. `Repositories` will return a hash for a package belonging to a
repository jmj does not front, which its `upstream_url` cannot serve. That path
is unreachable under ADR-007 — pkg never asks — but a successful hash lookup is
**not** proof the upstream will answer, and the rework must not assume it is.

### 4.7 What the two ADR-002 config keys are called — **RULED** (owner, 2026-08-09)

ADR-002 decided the mechanism — a global cap and a per-remote-IP cap, both
non-blocking, both defaulting to `0` = unlimited, `503` when either is full —
and deliberately left the two settings unnamed. §5.3 cannot write the mechanism
without naming them, and inventing a config key is exactly what ground rule 3
forbids, so this was put to the owner.

**Ruled: `max_concurrent_seeds` and `max_concurrent_seeds_per_ip`.** Both `int`,
both defaulting to `0` meaning unlimited, both in jmj's own config. **Do not
rename them** — a rename does not fail loudly, it silently reverts an
operator's cap to unlimited, which is the one outcome the cap exists to
prevent.

This section is the citable source, because ADR-002 is not: it rules on the
mechanism and says nothing about spelling. `internal/config/config.go` and
`internal/peer/serve.go` both cite §4.7.

**Implemented** with §5.3. Negative values are refused (`0` already says
unlimited, so a negative is a mistake), and a per-IP cap larger than the global
one is warned about rather than corrected — it can never fire, so it is dead
configuration and most likely a transposition, but which number the operator
meant is not ours to guess.

*Recorded by the implementing agent from the ruling as relayed, because the
section it was cited to did not exist in the tree. The wording of the decision
is the owner's; the surrounding prose is not.*

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

### 5.7 Facade rework under ADR-003/005/006 — **UNBLOCKED as of 2026-08-08**

> **The two rulings this section waited on have landed** — §4.4 as ADR-005
> (the facade proxies metadata) and §4.5 as ADR-006 (`upstream_url`, required,
> no default, `${ABI}` expanded at startup). The "do not start" below is
> **lifted**; the description of *why the file is wrong* is still accurate and
> is why it needs a rewrite rather than an edit.
>
> Scope, now fully specified: the upstream fetch path (streaming, no spool, no
> `[]byte`), the metadata branch (relay from upstream, including `304`, instead
> of today's `404`), the narrowed `404`/`502` semantics, `If-Modified-Since`
> relay, safe joining of a client-supplied path onto the upstream base, and the
> contract comment at the top of the file. The tests that encode the retired
> contract go with it — `facade_test.go:91`, `:189`, `:354`, and
> `daemon_test.go:187`, which uses a metadata path as its probe.
>
> `cfg.UpstreamURL` is ready to consume: populated, validated, expanded.

#### Historical framing (kept — it explains why the file is wrong)

**`internal/daemon/facade.go` implements a model that has been measured false
and is not to be extended, tuned or partially migrated until the two rulings
below land.** The file is not broken in the sense of failing its tests — it
does exactly what the old spec said — but what the old spec said does not work
against real pkg. Treat it as frozen, not as a starting point.

The specific mismatch: on a peer miss it returns an HTTP error, on the
assumption that pkg falls through to another mirror. §7.1 measured that it does
not — a facade error ends the install. Under ADR-003 that path must instead
fetch from a configured upstream mirror and stream the bytes through.

**Blocked on:**

| Blocker | Why it blocks this file |
|---|---|
| ~~§4.4 — does the facade proxy pkg's catalogue?~~ | **RULED — ADR-005: it proxies.** The non-package branch is now specified: fetch from upstream and relay. No longer a blocker. |
| ~~§4.5 — how the upstream mirror is configured~~ | **RULED — ADR-006, and implemented.** `cfg.UpstreamURL` is populated, validated and `${ABI}`-expanded by the time the daemon starts. No longer a blocker. |
| ~~§4.6 — one `upstream_url` vs. several enabled repositories~~ | **RULED — ADR-007: jmj fronts one repository and coexists with the rest.** The question rested on a misreading of ADR-003 (*mirror* ≠ *repository*). Config schema unchanged. Never blocked anything. |

**Nothing blocks this file any more.** It is frozen only in the sense that it
has not been rewritten yet — the rulings it was waiting on have all landed.

**Not blocked on §5.3**, and §5.3 does not depend on this — they are different
wires. §5.3 is the work to pick up.

When it unblocks, the scope is: the upstream fetch path (streaming, no spool,
no `[]byte` — ADR-003), the narrowed `404`/`502` semantics, `If-Modified-Since`
relay (§6), and the contract comment at the top of the file. The document
corrections that used to be listed here are **done** — see §1.

A marker is in the file itself; `grep -rn 'BLOCKED (HANDOFF §5.7)' internal/`
finds it.

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
- **`internal/daemon/facade.go` is BLOCKED and implements a superseded model.**
  Its tests pass, which is misleading: they encode the old contract, so green
  tests mean it is consistently wrong rather than correct. Frozen until §4.5 is
  ruled — see §5.7. Note that ADR-005 has now made the *metadata* branch a known
  defect rather than an open question: it answers `404`, which §7.1 measured
  breaks `pkg update` outright, and it must relay from upstream instead. The
  tests that encode the refusal (`facade_test.go:91`, `:189`, `:354`, and
  `daemon_test.go:187`, which uses a metadata path as its probe) go with it.
- **The facade has no answer for `If-Modified-Since`.** pkg sends conditional
  `GET`s for catalogue files. Ignoring the header wastes catalogue bandwidth on
  every `pkg update`; answering `304` from a guess would serve a stale
  catalogue. ADR-003's proxying resolves this for free and ADR-005 now requires
  the relay explicitly, but it is unimplemented.

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
