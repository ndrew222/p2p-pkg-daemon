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
- **Run `go test ./... -race -count=2` before requesting a merge.** Not part of
  the gate above, and this is why it is called out separately: §5.3 merged green
  with a data race in `startDiscoveryLocked` that the plain gate cannot see. It
  was found only because §5.7 ran `-race` afterwards, and it was intermittent
  even then — two runs in three. Concurrency is now everywhere in this codebase
  (two HTTP servers, two semaphores, a watcher goroutine, SIGHUP reload), so the
  plain gate is no longer sufficient evidence on its own.
- **Work log required.** `docs/logs/<author>-<feature>.md` for every feature,
  including your areas of uncertainty and whether you raised them.

Current branch: `main`, and everything described in this document is merged
into it. *(This line used to pin a commit hash, which went stale at every merge
and twice told a new agent the tree was further behind than it was. Run
`git log --oneline -5` instead — it is authoritative and this line cannot be.)*

ADR-001 through -010 are all Approved. **§5.2 (the reload trigger), §5.3 (the
peer wire migration and the cache-backed seeder), §5.4 (mounting both servers)
and §5.7 (the facade rework) are all done.** There is no large open work item.

**Want to see it work before you change it?**
`docs/logs/claude-demo-guide.md` is every demo in the project, ordered by what
it costs to run: five seconds and no dependencies at one end, two machines at
the other. Every transcript in it was produced on the date it names.

**Every item in §4 is closed.** §4.4, §4.5 and §4.6 by ADR-005, ADR-006 and
ADR-007 (2026-08-08); §4.7, the two ADR-002 config key names, by owner ruling
(2026-08-09); and **all four of §4.8's judgement calls ruled 2026-08-09** — (a)
upheld and made binding by ADR-009, (c) overturned so that pkg's `User-Agent` is
relayed, (b) and (d) ratified exactly as they shipped.

**Nothing is with the owner as a question.** §5.3's one open item — whether a
non-exact path on the peer wire is `400` or `404` — was **ruled `400`
(2026-08-10)**, which is what shipped. This document is the citable source, as
it is for §4.7; the spec's prose still says `404` in one place and only the owner
can edit `docs/`, so it is listed under §9 with replacement text.

Both items the host round raised were ruled on 2026-08-10 and are fixed —
**§4.9** (the watcher ignores pkg's lock file; ADR-008 carries a dated
amendment) and **§4.10** (the daemon prefers its own repository's rows;
ADR-010). Both fixes were **re-verified on the host on 2026-08-10** by a later
session: `docs/logs/claude-host-round-fixes.md` is their work log and
`docs/logs/claude-demo-guide.md` §2.5 is the measurement.

**The FreeBSD host round is done (2026-08-09) and closed nearly everything it
was holding**: the suite passes on the target OS, ADR-008's platform assumption
is confirmed by measurement, `pkg update` and a real `pkg install` work end to
end through the facade, and the `mirror_type: http` bug report is complete and
fileable. See §7.6–§7.9 and `docs/logs/claude-freebsd-host-round.md`.

**The two-machine trial is done (2026-08-10, §7.10 and §7.11).** Three machines,
two of them public FreeBSD boxes: transfers in both directions, selection among
several holders, a **hostile peer serving a same-size forgery** — caught on hash,
blacklisted, and the caller still served correctly — recovery from a corrupt peer
*within* the swarm, ADR-001's asymmetry for a NAT'd peer, ADR-002's `503` under a
cap, a tracker on a machine that is neither peer, and **98 MB moved at 43.5 MB/s
with the requester's RSS up 20 KiB and the seeder's unchanged**. Work log:
`docs/logs/claude-two-machine-trial.md`. **Nothing failed and nothing surprised.**

What remains untested is narrower and listed at `claude-demo-guide.md` §3.4.1: a
genuinely slow link, an interrupted transfer, more than three holders.

Read before touching the facade: ADR-003 (fetch semantics), ADR-004 (path
rule), ADR-005 (metadata is proxied), ADR-006 (`upstream_url`) and ADR-007 (jmj
fronts one repository and coexists with the rest). `upstream_url` is now
consumed. ADR-007's trap survives the rework and is honoured in code rather
than assumed away: a successful repository-database lookup is **not** proof the
upstream can serve that package.

## 1. Document map — what to trust

### Authoritative

| Document | Status |
|---|---|
| `AGENTS.md` | Current. Constraints and precedence. ADRs are rank 1. |
| `docs/adr/adr-001-transport-nat.md` | **Approved.** No NAT traversal; plain HTTP over TCP to the advertised IP:port. |
| `docs/adr/adr-002-serving-side-concurrency.md` | **Approved.** Global *and* per-remote-IP semaphores, `503` when either is full, default `0` = unlimited. **Implemented** with §5.3; the two key names are §4.7. |
| `docs/adr/adr-003-facade-fetch-semantics.md` | **Approved.** Facade proxies to upstream on a peer miss; peer path spools, upstream path streams; no facade cache. |
| `docs/adr/adr-004-facade-path-rule.md` | **Approved.** Carries the `All/` + `Hashed/` + `~hash10` path rule out of the deprecated facade spec. Introduces no new decision; if it differs from that spec's text, the spec wins. |
| `docs/adr/adr-005-metadata-proxying.md` | **Approved.** The facade proxies non-package paths to the configured upstream and relays the response, including `304`. Closes §4.4; retires *"never proxies metadata"*. |
| `docs/adr/adr-006-upstream-mirror-config.md` | **Approved and implemented** (the key, not its consumer). `upstream_url` in jmj's config: required, no default, `${ABI}` expanded at startup, plaintext warned not refused. Closes §4.5. |
| `docs/adr/adr-007-repository-topology.md` | **Approved.** jmj fronts one repository, replaces that one, coexists with every other enabled repository. `upstream_url` stays singular. Closes §4.6; corrects a misreading of ADR-003 in §4.6 and ADR-006. |
| `docs/adr/adr-008-repository-reload-trigger.md` | **Approved and implemented.** `fsnotify` on `repo_db_dir` triggers `Repositories.Reload`; directories watched not files, two-second settle, runtime failure keeps the old snapshot, a successful reload re-announces. Closes the §5.2 follow-up. |
| `docs/adr/adr-009-facade-status-set.md` | **Approved.** The facade's status set is exhaustive and has no `500`; `peer.ErrSpool` is a peer-path failure and goes to upstream. Closes §4.8(a) and settles the §5.3/§5.7 disagreement. |
| `docs/adr/adr-010-own-catalogue-preference.md` | **Approved.** On a name-version collision the row from this daemon's own repository wins, identified by a loopback source URL; path order remains the fallback. Closes §4.10. |
| `docs/tracker-protocol-spec-v0.2.md` | Current **and implemented**. daemon↔tracker. |
| `docs/peer-transfer-spec-v0.2.md` | Current **and implemented** (§5.3, mounted in §5.4). Its migration table and definition of done are both discharged. One self-contradiction is open with the owner — `400` vs `404` for a non-exact path; see §5.3. |
| `docs/uc-05.puml`, `docs/keepalive.md` | Current and implemented. |
| `docs/uc-07.puml` | Current **and implemented** as of §5.7. Carries the relay flow, the 304 branch and the terminal `502`; `internal/daemon/facade.go` is the code for it. |
| `docs/uc-01.puml`, `cmd/jmj/README.md` | Current as of the two-address config and `repo_db_dir`. |
| `docs/uc-06.puml` | Current as of the HTTP peer wire, **and implemented** (§5.3/§5.4). |

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
  `facade_addr`**, reworked under ADR-003/004/005/006/007 (§5.7). Peer path
  spools through `temp_dir` via `peer.FetchFirst` and serves from the handle it
  returns; upstream path streams from `upstream_url`; non-package paths are
  relayed, `If-Modified-Since` and all. Skips and marks blacklisted peers.
- `internal/peer` — fetch and seed over the v0.2 HTTP wire (§5.3). The seeder
  is an `http.Server` with ADR-002's two caps; the requester spools to
  `temp_dir` and returns an open file. `internal/peerwire` is deleted.
- `internal/peer/blacklist.go` — the local peer blacklist (§5.5). In-memory, no
  expiry, not persisted.

**The seed half now exists.** `internal/daemon/cachesource.go` is the
production `peer.PackageSource`: it opens `<cache_dir>/<name-version>.pkg`
read-only and hands back the handle, so the seeder streams from the pkg cache
and never holds a package. It is also the path-safety boundary for a
name-version arriving off the wire — `peer.validName` deliberately is not one.

§5.3 was a build, not just a migration, and it is done — including the wiring
(§5.4). The daemon now listens on `serving_addr` and serves what it announces.

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

### 4.7 What ADR-002's two concurrency limits are called — **RULED** (owner, 2026-08-09)

ADR-002 says both caps "are configurable" and names neither key, and
`internal/config` has no key for either. That is a gap of exactly the kind
ground rule 3 forbids an agent filling — §4.5 spent a session on the same shape
of question — so it was put to the owner rather than guessed.

**Ruled:**

| Key | Meaning | Type | Default |
|---|---|---|---|
| `max_concurrent_seeds` | Global cap on simultaneous seeds | int | `0` = unlimited |
| `max_concurrent_seeds_per_ip` | Per-remote-IP cap | int | `0` = unlimited |

`_per_ip` names the identity source deliberately, and it is the one ADR-002
already fixes: the host half of `r.RemoteAddr` via `net.SplitHostPort`, **never**
a header. `0` means the default behaviour is unchanged and an operator opts in,
which is ADR-002's own position — the hostile-peer expectation justifies the
mechanism, not a number, and nobody has measured one.

**Do not rename them.** A rename does not fail loudly — it silently reverts an
operator's cap to unlimited, which is the one outcome the cap exists to prevent.

This section is the citable source, because ADR-002 is not: it rules on the
mechanism and says nothing about spelling. The ADR itself is unchanged, since
`docs/` is spec territory and this is a naming ruling, not a new decision.
`internal/config/config.go` and `internal/peer/serve.go` both cite §4.7.

**Implemented** with §5.3. Negative values are refused (`0` already says
unlimited, so a negative is a mistake), and a per-IP cap larger than the global
one is warned about rather than corrected — it can never fire, so it is dead
configuration and most likely a transposition, but which number the operator
meant is not ours to guess.

### 4.8 Four judgement calls the §5.7 rework made — **ALL FOUR RULED 2026-08-09**

Raised 2026-08-09 by the facade rework and ruled the same day. **None of these
blocked anything** and none was a silent resolution of a stated ambiguity: each
was a place where the ADRs settle the *rule* and leave a mechanism detail
unstated, and the choice made was recorded here so it could be ratified or
overturned rather than inherited. Full reasoning in
`docs/logs/claude-facade-rework.md`; the rulings in
`docs/logs/claude-facade-ratifications.md`.

The outcome, and it is worth noting that recording them was not a formality —
one of the four was overturned:

| # | Ruling |
|---|---|
| a | **Upheld and made binding.** `docs/adr/adr-009-facade-status-set.md` (Approved). |
| b | **Ratified as shipped.** The query string is relayed. No code change. |
| c | **Overturned.** `User-Agent` is relayed too, on both paths. |
| d | **Ratified as shipped.** Transparent gzip stays disabled. No code change. |

| # | Call made | Why, and what overturning it costs |
|---|---|---|
| a | **`500` is no longer a facade status.** An unwritable `temp_dir` (`peer.ErrSpool`) used to be a `500`; it now falls through to upstream like any other peer-path failure. **§5.3 took the opposite view** — see below. **RULED 2026-08-09: upheld, and `docs/adr/adr-009-facade-status-set.md` makes it binding.** | ADR-003's rebuilt table has no `500` row, and its governing rule says *every* peer-side failure goes to upstream and an error reaches pkg only when both sources are gone. The upstream path does not touch `temp_dir`, so it can still serve. Overturning it means pkg fails an install the daemon could have served, in exchange for a louder signal about a broken `temp_dir` — which is in the log either way. |
| b | **The query string is relayed** on the metadata branch. **RATIFIED 2026-08-09 as shipped** — the branch relays the *request*, not merely the path. No code change. | Faithful relay of what pkg asked for. No ADR mentions queries and no measured pkg request carried one, so this is unobservable today; it costs nothing either way and is stated only because it is a difference between "relay the path" and "relay the request". Note the asymmetry that survives: the **package** path drops the query (`facade.go` passes `""`), because a package request is reduced to a validated name-version before anything is fetched and relaying a query on it would make the upstream request differ from what a peer was asked for. |
| c | ~~**Exactly one request header is forwarded upstream** — `If-Modified-Since`, the one ADR-005 names. Not `User-Agent`, not `Accept-Encoding`, not `Range`.~~ **OVERTURNED 2026-08-09: `User-Agent` is relayed verbatim too**, on both the metadata and the upstream-package paths, and suppressed entirely when pkg sent none rather than falling back to Go's default. | The cost recorded here was real — mirror operators saw Go's default rather than pkg's — and the owner ruled the facade should present as the pkg client it is fronting. Relaying beats hardcoding because there are **two** measured strings, not one: `pkg/2.7.5` on catalogue requests and `fetch libfetch/2.0` on package fetches (§7.3). A relayed header invents nothing and goes stale with nothing. It reaches the package path as well as the metadata path because it does not change the bytes, so the peer-versus-upstream symmetry argument that keeps `If-Modified-Since` off that path does not apply to it. `TestFacadeRelaysTheUserAgent`. `Range` and `Accept-Encoding` stay unforwarded — see (d). |
| d | **Transparent gzip is disabled on the upstream client.** **RATIFIED 2026-08-09 as shipped.** No code change. | Left on, Go's transport adds its own `Accept-Encoding`, gunzips the response and drops `Content-Length` — so the facade would hand pkg bytes that are not the bytes upstream sent, which ADR-005's "unmodified" forbids and which on the package path could not match `packages.cksum`. The cost is that catalogue transfers are not compressed in transit. **§5.3 reached the same conclusion independently** on the peer wire, for the same reason. |

#### (a) was a disagreement between two pieces of work — **CLOSED (ADR-009)**

**Ruled 2026-08-09: `facade.go` is binding.** `peer.ErrSpool` goes to upstream,
`500` is not a facade status, and `docs/adr/adr-009-facade-status-set.md`
(Approved) states the whole status set as exhaustive rather than exempting this
one case — so a future condition that genuinely needs to tell pkg "this daemon
is broken" needs an ADR reopening the table, not a spare `5xx`.

Three things changed, and between them they are why this cannot quietly revert:

- `internal/peer/fetch.go`'s `ErrSpool` comment no longer claims the facade
  answers `5xx`. It keeps its real reason — the fetch loop stops rather than
  blaming every holder for this daemon's fault — and says the caller owns the
  status code.
- `internal/daemon/facade.go`'s header cites ADR-009 instead of flagging an open
  question.
- Two tests pin the behaviour: `TestFacadeUnwritableTempDirGoesToUpstream`
  (`200` from upstream) and `TestFacadeUnwritableTempDirWithNoUpstreamIs502`
  (`502`, never `500`, when both sources are gone).

The accepted cost, stated in ADR-009: a daemon with a broken `temp_dir` keeps
installs working and stops participating in the swarm, and the log is the only
signal. No alarm, health endpoint or startup probe was added to compensate —
none is specified anywhere.

The original statement of the problem follows.

---

Worth separating from the other three, because two shipped components now say
opposite things and only one can be right.

- `internal/peer`'s `ErrSpool` doc: the error is distinguished *"so the facade
  can answer 5xx — 'this daemon is broken' — rather than 'no peer has it'"*,
  and §5.3's interim edit to `facade.go` did answer `500`.
- `internal/daemon/facade.go` as reworked: `ErrSpool` goes to upstream, because
  ADR-003's table has no `500` and its governing rule reserves an error to pkg
  for the case where **both** sources have failed. A broken `temp_dir` does not
  stop the upstream path.

Neither position is an ADR. **The facade owns the status codes**, so its reading
is what shipped, and the disagreement is recorded here rather than resolved by
whichever file was written second. What is at stake: with the fall-through, a
daemon whose `temp_dir` is broken keeps installs working and degrades silently
into a plain proxy (loudly logged, but only logged); with the `500`, the
operator finds out immediately and every install fails until they fix it.
**Unblocks:** one sentence from the owner. **Cost of leaving it:** the two files
disagree in comments, and a later reader may "fix" one to match the other
without knowing a choice was made.

### 4.9 One spurious catalogue reload per `pkg update` — **RULED 2026-08-10. Fixed.**

**Ruled: ignore the files the daemon never reads.** `<repo>/lock` and
`<repo>/meta` no longer arm the settle timer; everything else still does,
whatever its op. Recorded as a dated amendment to
`docs/adr/adr-008-repository-reload-trigger.md`, which had said this is exactly
what changes if the measurement contradicted the mechanism.

The rule is about **us**, not about pkg: `Reload` reads `<repo>/db` and nothing
else, so a change confined to a file we never open cannot alter the snapshot and
a reload owed to one is owed to nothing. That is checkable against our own code.
"Which files pkg writes during an update" would be the guess the original ADR
wording rightly refused — pkg writes the catalogue during an update too.

**The first attempt excluded only `lock`, and did not work.** It was committed
with passing tests; re-measuring on the host showed two reloads per update
twenty seconds apart, because `meta` is written immediately after the lock and
armed the timer before the same 11.2-second download silence. Verified fixed on
the host afterwards: **one reload per `pkg update`.**

Tests: `TestRepoWatcherIgnoresFilesItNeverReads` (table-driven over the ignored
set; each name alone reloads nothing and does not consume the reload a later
catalogue write is owed) and `TestRepoWatcherReloadsOncePerUpdateSequence`,
which asserts the whole measured sequence — lock, meta, silence, temp file,
rewrite — produces **exactly one** reload. The second is the one that matters:
the first attempt passed every test that checked the exclusion in isolation.

The original statement of the problem follows.

---

Found by measurement on the reference host, not by reading the code. Full data
in `docs/logs/claude-freebsd-host-round.md` §2.

`pkg update` touches `lock` and `meta` under `repo_db_dir`, then **goes quiet
for 11.2 seconds** while it downloads, and only then rewrites the catalogue.
ADR-008's two-second settle therefore fires inside that gap and reloads the
*old* catalogue — completely and correctly, and pointlessly, since it was
already loaded. The real rewrite follows and gets its own, useful reload.

**Cost:** one wasted 38,052-row reload and one spurious re-announce per update.
Not a correctness problem — the snapshot is right at the end either way, and the
2s settle still cannot fire mid-rewrite (the largest gap inside a rewrite is
0.48s, measured).

**The fix is small:** do not arm the timer for events on `lock`, or arm it only
for `db`. **It is not mine to make**, because ADR-008 states the opposite as a
mechanism — *"Every event under `repo_db_dir` counts, whatever its op. The
watcher does not try to tell a catalogue rewrite from a journal file being
created"* — and that sentence was written deliberately, to avoid guessing at
pkg's file layout. Narrowing it now would be guessing with better data, which is
still a rule change.

**Unblocks:** one sentence, plus an amendment to ADR-008's mechanism section.
**Cost of leaving it:** a wasted reload per update, and a log line claiming a
reload eleven seconds before anything changed, which will mislead somebody
reading logs.

### 4.10 jmj's own catalogue lands inside `repo_db_dir` — **RULED 2026-08-10. Fixed.**

**Ruled: prefer our own catalogue's rows**, recorded as
`docs/adr/adr-010-own-catalogue-preference.md` (Approved) and implemented in
`internal/daemon/repodb.go`.

Not a tie-break. **pkg resolved the package from the jmj repository**, so jmj's
row is the one pkg is acting on and the one the bytes it re-verifies must match;
using a repository pkg never consulted is the wrong choice, not a neutral one.
The other obvious option — skip our own catalogue — keeps the rows pkg did not
use and discards the ones it did.

Mechanically it is one reordering: `ownCatalogueFirst` puts the loopback-sourced
catalogue in front and the existing first-wins merge does the rest. Identifying
"ours" needed nothing new — `upstreamcheck.go` already reads
`repodata.packagesite` and already carries the concept in a comment written
before this problem was found. **With no loopback catalogue the order is
unchanged**, so every host that has not adopted jmj, and this one's first start
before pkg writes our catalogue, behave exactly as before.

**The collision log changed with it.** Reporting every duplicate meant 37,813
lines per reload in the normal deployment. It now compares the rows and reports
only genuine disagreement in `cksum` or `pkgsize` — silent today, and a precise
alarm on exactly the drift that would otherwise have us blacklisting an honest
peer.

Tests: `TestOwnCatalogueWinsACollision`,
`TestPathOrderStillDecidesWithoutAnOwnCatalogue`,
`TestAgreeingDuplicatesAreNotLogged`, `TestDisagreeingDuplicatesAreLoggedOnce`,
and `TestOwnCatalogueFirst` (table-driven, including several loopback catalogues
and an unparsable source).

**ADR-007's trap is untouched:** this narrows *which row* is used, not *whether*
the package exists, so a successful lookup is still not proof `upstream_url` can
serve that package.

The original statement of the problem follows.

---

Also from the host round, and it only appears in a real deployment: configuring
jmj as a pkg repository makes pkg create `/var/db/pkg/repos/jmj/db` — **inside
the directory jmj scans.** Measured with the stock repositories left enabled,
which is ADR-007's coexistence:

```
daemon: loaded 37813 package(s) from /var/db/pkg/repos/jmj/db
daemon: 37813 name-version(s) appear in more than one repository;
        the first in path order won: 0ad-0.28.0_4, … and 37803 more
```

Nothing broke. The collision resolution is deterministic and already ratified,
`FreeBSD-ports` sorts first, and both catalogues came from the same upstream so
their hashes agree.

**The risk is drift.** The two catalogues are fetched at different times — jmj's
through the facade, `FreeBSD-ports`'s directly — so they can hold different
`pkgsize`/`cksum` for the same name-version after a repository rebuild (which
§4.9's round measured happening: 16 of 20 cached packages changed size with no
version bump). Path order would then verify pkg's bytes against the *other*
repository's row. That degrades to a failed install, never a corrupt one — UC-02
§10 has pkg re-verify — but it is a failure mode nothing currently anticipates.

Options, none obviously right: ignore a catalogue whose `repodata` source URL is
our own `upstream_url`; make the daemon skip a repository directory named in
config; do nothing and document it. **What it needs:** a ruling. **Cost of
leaving it:** a rare, confusing install failure after a rebuild, on exactly the
hosts that adopted jmj.

## 5. Work, in order

### 5.1 Config schema — **DONE**

### 5.2 Repository database reader — **DONE**

~~One follow-up remains: **nothing triggers `Reload()`**.~~ **Closed 2026-08-09.**
`pkg update` rewrites the catalogues, so a long-running daemon went stale and
started answering `404` for packages added since startup — and under ADR-003
that `404` ends the install rather than falling through.

~~Choosing between a watch on `repo_db_dir` and a periodic reload is a design
decision in no spec — **ask before picking**.~~ **Ruled: `fsnotify`**, recorded
as `docs/adr/adr-008-repository-reload-trigger.md` (Approved) and implemented as
`internal/daemon/repowatcher.go`. Work log:
`docs/logs/claude-repodb-reload.md`.

Four things about it that are decisions, not incidentals:

- **Directories are watched, never the `db` files.** On kqueue a file watch
  follows the inode, so a catalogue replaced by a rename would leave the watch
  pointing at a dead file, silently and for the life of the process. The watch
  set is also rebuilt after every reload attempt, which is what makes it correct
  on inotify and kqueue without depending on either one's rename semantics.
- **`repo_db_dir` stays read-only.** A missing directory is refused, never
  created. `TestRepoWatcherStartRefusesAndCreatesNothing` is the regression test.
- **A runtime reload failure keeps the previous snapshot** and does not
  re-announce. `Repositories.Reload` already returned before its swap on every
  error path; that is now stated in its doc comment and pinned by
  `TestReloadFailureKeepsThePreviousSnapshot`.
- **A successful reload re-announces**, because the keep-alive rescans the cache
  and re-applies `SanityFilter` on every announce. This is the case the ruling
  was actually about: a cached file dropped for disagreeing with the *superseded*
  sizes is never revisited by a cache event, since nothing about the file
  changed. `TestCatalogueRewriteReachesTheTracker` covers it end to end.

The watcher's lifetime is discovery's — SIGHUP already restarts discovery on a
superset of the conditions that stale it. It holds the `*Repositories` and the
nudge closure it was given rather than reading `d.repo` or `d.reannounce` from
its own goroutine, which is the §5.3 race shape and is why `-race -count=2` ran
green on it before the merge.

### 5.3 Peer wire migration — **DONE**

Implemented to `docs/peer-transfer-spec-v0.2.md`'s migration table and
definition of done; work log at `docs/logs/claude-peer-wire-v0.2.md`.
`internal/peerwire` is deleted, `peer.Server` is an `http.Server`,
`FetchFromPeer` returns an open `*os.File`, `PackageSource` returns an open
handle and a size, `cmd/demo` runs the real wire, and the cache-backed seeder
exists at `internal/daemon/cachesource.go`. Mounting it on `serving_addr` is
§5.4, which is where that half is recorded.

~~**One item is with the owner**~~ — **RULED 2026-08-10: `400` is canonical**,
which is what shipped. The spec's *Request surface* section says a non-exact
path is a `404` while its *Responses* table says `400`; the table wins. The
reason the implementation chose it stands as the reason the ruling did: `404`
carries the UC-06 §5b re-announce obligation, so answering it to a malformed
path would let a hostile peer drive our announce traffic, and a malformed path
is no evidence about what this daemon holds either way.

**This section is the citable source**, as §4.7 is for ADR-002's key names — the
spec is self-contradictory and `docs/` is not ours to edit. The prose half is
listed at §9 with replacement text. No code change: `internal/peer/serve.go` is
already correct, and the demo guide §2.6 shows the `400` and the `404` side by
side against real pkg.

The original statement of the work follows.

---

Work to `docs/peer-transfer-spec-v0.2.md`'s migration table and definition of
done. Deletes `internal/peerwire`; rewrites `peer.Server`, `FetchFromPeer`,
`PackageSource`, `cmd/demo` and both peer test files; **and builds the
cache-backed seeder that has never existed** (§2).

Three things it must carry that are easy to lose:

- **Both ADR-002 semaphores** — global and per-remote-IP — replying `503`.
  Remote identity is the host half of `r.RemoteAddr` via `net.SplitHostPort`,
  never a header. `503` must not trigger the UC-06 §5b re-announce, and must not
  carry `Retry-After`. The config keys are **`max_concurrent_seeds`** and
  **`max_concurrent_seeds_per_ip`**, ruled at §4.7 — do not rename them.
- **The size bound as real code**: `io.LimitReader(body, expectedSize+1)` plus
  the `Content-Length` check.
- **Constant memory on both ends.** A `[]byte` in either signature is a
  regression, and is what currently OOMs a 1 GiB host on the 2.83 GiB package.

~~**Do not delete `internal/peerwire` before that size bound is in place.**~~
`MaxPayload` was the only length check on the fetch path, so the package could
not go before its replacement did. It went in the same change, as required:
`io.LimitReader(body, want.Size+1)` plus the `Content-Length` check now stand
between the fetch loop and a hostile peer, and they are stricter than the
constant ever was — exact, per package, and with no ceiling.

**§5.3 and §5.7 were worked concurrently** (owner direction, 2026-08-09), in two
worktrees, and merged §5.3-first because it owns the `FetchFromPeer` signature.
The one seam was `facade.go`'s peer-fetch call site: §5.3 changed the fetch to
return an open `*os.File` rather than `[]byte`, and §5.7 rewrote the caller.
Both sides coded against the migration table's signature rather than agreeing
one between themselves, which is why the merge was mechanical.

### 5.4 Mount the facade and the seed server — **DONE**

Both halves are mounted. The facade is on `facade_addr` (loopback-enforced) and
the seed server on `serving_addr` (public, and its port is what the tracker
advertises for us) — two listeners, deliberately, because the peer namespace is
unlike the facade's precisely so a seeding daemon cannot be used as a pkg
mirror, and one mux would undo that with a single entry.

`Daemon.startSeedServerLocked` binds `serving_addr` synchronously, so a port it
cannot take is reported to the caller instead of logged from a goroutine after
startup has claimed success. `Daemon.stopSeedServerLocked` closes the server and
the listener, in that order and unconditionally: `Serve` runs in a goroutine
that may not have started yet, and without the listener close SIGHUP left the
old address bound so the new one could never take over.

The seeder's `404` is wired to the keep-alive's re-announce nudge, which closes
the UC-06 §5b obligation end to end. `503` is not wired to it, and must not be.

Trap for whoever touches this: a nil
`*Repositories` assigned into an interface field is a **non-nil interface
holding a nil pointer**, so every `== nil` check downstream passes and the first
call panics. Both wiring sites go through `Daemon.repository()` for this reason;
`TestStartHTTPServerRefusesWithoutARepositoryDatabase` is the regression test.
The seed server avoids the same trap by *constructing* its `PackageSource`
rather than assigning a possibly-nil pointer into the interface field.

### 5.5 Local peer blacklist — **DONE**

### 5.6 §4.1 cache-layout cross-check — **CLOSED** by §7.5.

### 5.7 Facade rework under ADR-003/005/006 — **DONE** (2026-08-09)

> **Delivered** on `worktree-facade-rework`, rebased onto §5.3; work log at
> `docs/logs/claude-facade-rework.md`. Every scope item landed:
>
> - Upstream fetch on a peer miss, **streamed** — no spool, no `[]byte`.
> - The metadata branch relays from upstream, with `If-Modified-Since`
>   forwarded and `304` relayed unchanged (never synthesised).
> - `404` narrowed to "provably absent from the repository database", `502` to
>   "peers *and* upstream both failed". `500` is gone — see §4.8(a), which is
>   the one open disagreement.
> - Safe join of a client-supplied path onto the upstream base
>   (`upstreamURL`), with a containment test table.
> - The contract comment at the top of the file rewritten around the five ADRs.
> - The four retired tests replaced, not deleted — `facade_test.go`'s metadata
>   `404`, `no peers` `404`, `all peers exhausted` `502` and the over-the-wire
>   metadata refusal, plus `daemon_test.go`'s probe, which now asserts a
>   relayed body (the old probe would pass against a facade wired to no
>   upstream at all).
>
> **The peer path is now streaming end to end**, which it was not while the two
> branches were in flight: `peer.FetchFirst` returns an open, rewound,
> already-verified file, the facade copies straight from that handle to pkg,
> and `peer.Discard` closes and removes it. No `[]byte` anywhere on the path,
> and the seam stand-in that stood in for this is gone.
>
> Open items are at **§4.8**, and the work log's *Uncertainties* section carries
> the reasoning.

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

~~A marker is in the file itself; `grep -rn 'SUPERSEDED (HANDOFF §5.7)'
internal/` finds it.~~ The marker is **gone with the rewrite**, along with the
`BLOCKED` string that preceded it, and with the `SEAM (HANDOFF §5.3` marker that
briefly replaced it. All three appear in commit messages and in this document
and find nothing in `internal/` now.

## 6. Known defects

- ~~`cmd/demo` depends on `peerwire` and on `PackageSource.Get` returning
  `[]byte`.~~ **Fixed in §5.3.** It now runs the real v0.2 wire end to end:
  `peer.Server` over `daemon.CacheSource`, fetched with `peer.FetchFromPeer`,
  with no `[]byte` on either side.
- ~~**The daemon announces a serving port nothing listens on.**~~ **Fixed in
  §5.3/§5.4.** The seed server is mounted on `serving_addr` and serves
  `GET /pkg/<name-version>` out of `cache_dir`. Kept here because it explains
  older trial logs: every peer acting on our tracker entry used to dial and get
  connection-refused, which correctly did *not* blacklist us — a dial failure
  never does — so it cost one wasted attempt per peer and was invisible on our
  side.
- ~~**`internal/daemon/facade.go` implements a superseded model.**~~ **FIXED by
  the §5.7 rewrite** (2026-08-09). It fetches from upstream on a peer miss and
  relays non-package paths, and the tests that encoded the retired contract were
  rewritten rather than deleted. With §5.3 merged there is **no residue**: the
  peer path streams end to end, from `peer.FetchFirst`'s open file straight to
  pkg, with no `[]byte` anywhere on it.
- ~~**The facade has no answer for `If-Modified-Since`.**~~ **FIXED** as part of
  §5.7's metadata branch. pkg's conditional `GET` is forwarded to the upstream
  mirror and upstream's `304` is relayed unchanged, never synthesised — the
  daemon tracks no upstream modification times, and a guess would serve a stale
  catalogue. Tested at `facade_test.go`'s
  `TestFacadeRelaysConditionalGetAnd304`.

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

~~Remaining unknowns, both minor: whether `cksum` is ever not sha256-hex~~
**Closed by §7.6 below.** ~~One unknown remains: where the tracker runs for a
real two-machine trial.~~ **Answered 2026-08-10 — §7.11.** The tracker needs a
publicly reachable address, not a particular OS; it ran on one of the peers and
then, in the last phase, on a box that was neither peer. A tracker behind NAT
needs a forwarded port, which is measured, not assumed.

### §7.6–§7.9 — the host round, 2026-08-09 — **ANSWERED**

Full evidence: **`docs/logs/claude-freebsd-host-round.md`**. Summary only.

6. **Is `cksum` ever not sha256-hex? No, on a second host.** All 38,052 rows of
   both catalogues checked independently of the daemon: zero bad `cksum`, zero
   non-positive `pkgsize`. The residual risk was always one host rather than one
   repository, and this is one more host with no exceptions.
7. **What does `pkg update` do to `repo_db_dir`?** **21,650 events**, and the
   catalogue is **replaced by a rename**. A watch on the `db` file itself fired
   exactly twice in the whole run — the rename — and then went permanently deaf,
   while the directory watch saw all ~21,600 subsequent writes. ADR-008's
   directory-watching decision is confirmed by measurement rather than argument,
   and it was not a marginal call. The largest gap *inside* a rewrite is 0.48s,
   so the two-second settle cannot fire mid-write. One spurious reload per
   update — §4.9.
8. **Does the whole thing work against a real pkg? Yes, end to end.**
   `pkg update` through the facade processed 37,813 packages with
   `signature_type: fingerprints` intact; `If-Modified-Since`/`304` relayed;
   a peer hit served 4,842,922 verified bytes from the cache over the peer wire;
   a peer miss went to upstream; `404` and `400` as specified; **`pkg install -y
   -r jmj tree` exited 0**; and the cache watcher took the announce from 4 to 5
   packages unprompted. `temp_dir` was empty afterwards. One deployment finding —
   §4.10.
9. **A host's shareable set decays as the repository is rebuilt.** Measured: 16
   of 20 cached packages had a `pkgsize` differing from the catalogue's by tens
   to a few thousand bytes, **same name-version, no version bump**, so only 4
   were announceable. `SanityFilter` is what stops those being announced and
   wasting a peer's transfer. Nothing in the design documents anticipates this
   decay, and it bounds how much a swarm can actually share.

**The `mirror_type: http` bug report is complete and fileable, and the owner
files it out of band (ruled 2026-08-10).** It is not a work item for this
repository and should not be carried as one. What it *is* worth recording is
what it means: `mirror_type: http` is the one mirror-ordering mechanism that
fits daemon-first selection (§7.2), and it crashes pkg 2.7.5 — so **the gap is
in FreeBSD's own infrastructure, not in this design**, and it is why ADR-007's
"jmj fronts one repository" is the only shape available rather than one choice
among three. Do not design around the crash and do not re-litigate the
mechanism; the report below is the record.

`docs/logs/freebsd-bug-report-pkg-mirror-type-http.md`. The child core's
backtrace puts the fault in **`fetchFreeURL` ← `libfetch_open`**, with frame 0
inside libc's allocator, on the first fetch after the mirror list is parsed.
Both isolation runs are done: `signature_type: none` still crashes, and a stock
`python3 -m http.server` still crashes, which exonerates §7's purpose-built
probe entirely. It also reproduces with jmj not running.

**Host access:** the owner has an SSH-accessible FreeBSD 15.1-RELEASE-p1 /
pkg 2.7.5 box and holds the address. *(Deliberately not recorded here — this
repository is public. Ask the owner.)* The host was returned to a verified clean
baseline after the §7 run and again after the 2026-08-09 round; a `pkg.core` was
left in place as evidence, and `/root/cores/` now holds two more.

**Working there costs nothing in setup.** The whole module cross-compiles —
`GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0` — because `modernc.org/sqlite` is pure
Go and `fsnotify` has a kqueue backend, and there are no `testdata` directories
or `exec.Command` calls in any test, so `go test -c` binaries are
self-contained. The host needs no Go toolchain and no source. `-race` is the one
exception: it needs cgo and a FreeBSD C toolchain, so it cannot be
cross-compiled and stays a Linux gate.

~~**A draft FreeBSD bug report exists** for the `mirror_type: http` segfault,
not yet filed. Before filing it needs: the **child** process's core … and two
isolation runs.~~ **All three are done (2026-08-09).** The report is complete
and fileable at `docs/logs/freebsd-bug-report-pkg-mirror-type-http.md`: the
child core's backtrace puts the fault in `fetchFreeURL` ← `libfetch_open`, and
both isolation runs still crash. Filing it needs a Bugzilla account, which is
why it is the owner's step and not this session's.

### §7.10 — two machines, 2026-08-10 — **ANSWERED**

**The two-machine trial is done.** It ran in two rounds the same day: first one
FreeBSD box and a NAT'd Linux box (items 10–13 below), then a second public
FreeBSD box supplied by the owner, which closed everything the first round left
open (§7.11). Work log: **`docs/logs/claude-two-machine-trial.md`**. Commands
and transcripts: **`docs/logs/claude-demo-guide.md` §3.**

**There is now no unproven claim in the design that a third machine would
settle.** What is left untested is narrower and stated at the guide's §3.4.1: a
genuinely slow link, an interrupted transfer, and more than three holders.

10. **A peer that is not `127.0.0.1` works.** A daemon on a Linux box, with an
    empty cache and a copy of the catalogue, fetched `fish-4.6.0_2` through its
    own facade from a FreeBSD peer at a real public address: **4,842,922 bytes,
    verified, 7.9 MB/s**, SHA-256 beginning `6f428aecbd` — matching the `~hash10`
    suffix on the seeder's cached file. The tracker registered both peers under
    their real public IPs with no configuration, because it keys on the
    connection's source address.
11. **ADR-001's asymmetry is real, and it costs five seconds.** The Linux box is
    behind NAT: it announced normally, and when it was the only holder the
    FreeBSD daemon dialled it, hit the 5-second `dialTimeout`, fell through to
    upstream and served correct bytes. **It was not blacklisted** — a dial
    failure never does that. "Every daemon can fetch, only reachable ones seed"
    had never been exercised before this.
12. ~~Still not proven, and it needs two *reachable* peers~~ — **all of it was
    covered the same day by §7.11**, on a second public box.
13. **The shareable-set decay of §7.9 reproduced unprompted**: the same host
    announced **4 of the 20 packages in its cache**, a day after the round that
    first measured 4 of 20. **Ruled 2026-08-10: this is a known bound, not an
    open question.** A host's shareable set decays as the repository is rebuilt,
    so a swarm is worth most in the burst case — many hosts installing the same
    thing in the same window — and least as a long-lived archive. Stated in the
    demo guide §3.5. `SanityFilter` is what keeps the stale copies out of the
    announce; a change that "optimises" it away would have peers fetching bytes
    that cannot verify.

### §7.11 — the full trial on two public boxes, 2026-08-10 — **ANSWERED**

Three machines: a tracker-and-daemon box, a second daemon box, and a NAT'd
fetcher. Full detail in `docs/logs/claude-two-machine-trial.md`; the numbers that
matter to someone changing this code:

14. **Constant memory is real, on a real transfer.** 98,852,086 bytes moved
    between two boxes in **2.27s (~43.5 MB/s)** while the requester's RSS went
    27,992 → **28,012 KiB (+20 KiB)** and the seeder's did not move at all.
    `temp_dir` empty afterwards. Until now the evidence for `AGENTS.md`'s
    constant-memory constraint was a code review and a 43-byte demo. **A `[]byte`
    in either signature would now regress a measured property.**
15. **A hostile peer is caught by the only thing that can catch it.** A forgery
    with the **right size and wrong bytes** is announced normally —
    `SanityFilter` compares sizes — so the requester's hash check is the entire
    defence, and it worked: the peer was blacklisted, the bytes were deleted, and
    **the caller still received the correct package** from upstream. With a
    second holder present the fetch instead **retried and succeeded from the
    swarm**, never touching upstream.
16. **The blacklist is whole-peer and it bites.** The request immediately after a
    mismatch, for a package that peer held *honestly*, was skipped without a
    dial. One bad package costs a peer all of its usefulness to that daemon until
    restart — documented, deliberate, and now observable.
17. **ADR-002's `503` degrades to upstream, not to failure.** With
    `max_concurrent_seeds: 1`, a second concurrent request was refused instantly
    with no queueing, the requester advanced, and **both callers got 200**.
18. **Peer order from the tracker is a map iteration and is effectively random.**
    Not a defect — nothing specifies an order — but a test that assumes one can
    pass without exercising what it claims to, which is how this round's first
    hostile-peer setup was built and had to be redone.

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

Added 2026-08-10, from the two-machine trial:

- **Do not assume the tracker's peer order.** `/peers` iterates a Go map, so the
  order is effectively random per query. A multi-holder test that relies on a
  particular peer being tried first can pass while never exercising the path it
  claims to — which is exactly how this round's first hostile-peer setup was
  built. Curate the holder set so only one path is possible.
- **A forged package has the right size.** `SanityFilter` compares sizes, so a
  same-size, wrong-bytes copy is announced normally and the requester's hash
  check is the *only* thing standing between it and the caller. Any change that
  weakens or defers that check removes the entire defence — and it was measured
  working, over a real link, in §7.11.

Added 2026-08-09, from the host round:

- **`pkg rquery "%sb"` is flatsize, not `pkgsize`.** It reports the *installed*
  size, so comparing a cached `.pkg` against it makes every package look
  mismatched by 3–6× and invents a defect that is not there. The size jmj
  verifies against is the `pkgsize` column, and reading it means reading the
  catalogue directly.
- **A cached package can stop matching its own catalogue entry with no version
  change.** The repository is rebuilt and `pkgsize`/`cksum` move under the same
  name-version; measured at 16 of 20 cached packages on the reference host.
  `SanityFilter` is what keeps those out of the announce, and a change that
  "optimises" it away would have peers fetching bytes that cannot verify.
- **Do not watch the `db` files.** Measured: a catalogue rewrite renames the
  file, so a watch on it fires once and then goes permanently deaf while ~21,600
  subsequent writes go unseen. Watch directories. This is ADR-008 and it now has
  evidence, not just an argument.

## 9. Spec edits waiting on the owner

Two places where `docs/` still contradicts a ruling. **Agents do not modify
specs** (`AGENTS.md`), so both are recorded here with the exact replacement text
rather than applied. Neither blocks anything and neither needs code: the code is
already on the ruled side of both.

Until they land, **this document is the citable source for both** — cite §5.3
and §4.3, not the spec text they contradict.

### 9.1 `docs/peer-transfer-spec-v0.2.md`, *Request surface* — `404` should be `400`

The third bullet under *Consequences of the split* says a non-exact path is a
`404`; the *Responses* table says `400`, and `400` was ruled canonical on
2026-08-10 (§5.3).

Replace:

> - There is no repo-path prefix to ignore. The path is exact; anything else is a
>   `404`.

with:

> - There is no repo-path prefix to ignore. The path is exact; anything else is a
>   `400`, per the *Responses* table. It is deliberately **not** a `404`: a `404`
>   carries the UC-06 §5b re-announce obligation, and a malformed path is no
>   evidence about what this daemon holds — answering one would let a hostile
>   peer drive our announce traffic. (Ruled 2026-08-10; `HANDOFF.md` §5.3.)

### 9.2 `docs/peer-transfer-spec-v0.2.md`, *Deliberately unspecified* — a row that is no longer open

The table still invites an agent to merge `PackageHashes` and
`RepositoryDatabase`, and even argues for it. That was decided the other way
(§4.3, owner) and the split is load-bearing: `SanityFilter` takes a **size-only**
interface, and that signature is what proves the announce path cannot hash
(`AGENTS.md`: *no hashing at announce time*). Merging them hands the watcher a
hash it is merely trusted to ignore. This matters more than a stale row usually
would, because `AGENTS.md` ground rule 3 points agents *at this table
specifically*.

Replace the row:

> | Whether `PackageHashes` and `RepositoryDatabase` merge | Open. They are two
> views of the same repository row and this spec relies on both being present
> together, which strengthens the case for merging. Not decided here. |

with:

> | Whether `PackageHashes` and `RepositoryDatabase` merge | **Decided — they
> stay separate**, composed as `daemon.Repository` where both are needed. Owner
> ruling, `HANDOFF.md` §4.3; rationale in `internal/daemon/repository.go`.
> `SanityFilter` takes the size-only interface so that the announce path
> *cannot* hash, which the merged type would not preserve. Do not merge them. |

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
