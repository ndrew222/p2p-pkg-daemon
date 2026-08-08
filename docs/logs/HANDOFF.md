# Handoff — instructions for the next agent

You are picking up a Go FreeBSD P2P package daemon mid-flight. This document
tells you what is decided, what is blocked, what is stale, and what to do next.
It is not a spec. Where it disagrees with a document in `docs/`, the document
wins — except where it says a document is stale, which is the point of §1.

Keep this file current. If you resolve something here, edit it in the same
commit as the work.

## 0. Rules that actually bite

- **Read `AGENTS.md` first.** Ground rule 2 is real and enforced: if a spec is
  silent or contradictory on something you need, **stop and ask**. Do not pick a
  reasonable interpretation and continue. The work logs in `docs/logs/` record
  every occasion an agent broke this rule and what it cost.
- **Atomic commits.** The owner reviews one commit at a time and has asked for
  this explicitly. One reviewable change per commit, with a message that
  explains *why*, not just what.
- **graphify.** `graphify query "<question>"` before reading or grepping source;
  `graphify update .` after changing code. See `CLAUDE.md`. **Caveat:** neither
  the binary nor `CLAUDE.md` is present in every environment — they were absent
  for the §5.1 work, which used ordinary search instead. The graph is stale for
  those commits. If you have the tool, re-run it.
- **Gate:** `go build ./... && go vet ./... && go test ./...`, plus `gofmt`.
  Tracker code and tests must run on any OS — no FreeBSD, no `pkg`, no second
  machine.
- **Work log required.** `docs/logs/<author>-<feature>.md` for every feature,
  including your areas of uncertainty and whether you raised them.

Current branch: `main`. The repository database reader (§5.2), the §4.3 merge
and the mounted facade (§5.4) landed there via PR #17 (`461e687`); the
`worktree-repo-db-reader` branch this file used to name is merged and gone.

The owner's four verification rulings — §4.3's two riders, §5.5's three
leftovers, and the unreachable size-mismatch branch — **have been executed**.
`docs/logs/claude-verification-rulings.md` is now an ordinary work
log recording what was decided and why; everything below has been edited to
match, so §4.3 and §5.5 no longer read as open. Nothing in it is outstanding.

The one thing that ruling round raised and did not settle — the §4.2
per-remote-IP cap — **has since been ruled on** and is written up as
`docs/adr/adr-002-serving-side-concurrency.md`. §5.3 is no longer blocked on it.

## 1. Document map — what to trust

### Current and authoritative

| Document | Status |
|---|---|
| `AGENTS.md` | Current. Constraints and precedence. |
| `docs/tracker-protocol-spec-v0.2.md` | Current **and implemented**. daemon↔tracker. |
| `docs/peer-transfer-spec-v0.2.md` | Current, **not implemented**. daemon↔daemon. Your main work item; it carries its own migration table and definition of done. |
| `docs/mirror-facade-spec-v0.1.md` | Current for what exists. Open questions 1, 3 and 5 are open; 2 and 4 are resolved. The path rule is now **ratified**, not implementer-drafted. |
| `docs/uc-02.puml`, `docs/uc-06.puml` | Current as of the HTTP peer wire. |
| `docs/uc-05.puml`, `docs/keepalive.md` | Current and implemented. |
| `docs/use-case-descriptions.md` | Current for UC-01, UC-02, UC-05, UC-06, UC-07. |
| `docs/uc-01.puml`, `cmd/jmj/README.md` | Current as of the two-address config and `repo_db_dir`. |
| `docs/adr/adr-001-transport-nat.md` | Current for what it decides: no NAT traversal, plain HTTP over TCP to the advertised IP:port. **But see the caveat below — its status line still says "Proposed".** |

**`docs/adr/` was missing from this map until now, and the omission hid a
contradiction.** `AGENTS.md` ground rule 1 makes an ADR one of the three things
a change may map to, and its hard-constraints list cites ADR-001 as settled
("no NAT traversal (ADR-001)") — yet ADR-001's own status reads *"Proposed
(drafted 2026-07-07; awaiting vetting by Andrew and Elroy)"*, and `docs/adr/`
appears nowhere in AGENTS.md's numbered precedence list. So an un-vetted
document is already being enforced as a hard constraint, and nothing says
whether an ADR outranks a use case or the reverse. **Two things for the owner:
vet ADR-001 (or mark it Accepted if that vetting already happened off-ticket),
and say where ADRs sit in precedence.** Listed here as existing; no precedence
rank has been invented for it.

### Stale — do not act on these without reading §3.1 first

| Document | What is wrong with it |
|---|---|
| `docs/logs/elroy-uc1-config.md` §"Decision 3" | **Actively misleading**, and now doubly so. It justifies `buffer_dir` as needing to "persist across reboots". That is wrong: the daemon has no store of its own and serves straight from the pkg cache, and the buffer is per-request and ephemeral. The field it describes no longer exists either — its settings table still lists `listen_addr` and `buffer_dir`. Left unedited because it is another author's work log; read it as history only. |
| `docs/protocol-spec-v0.1.md` | Historical. Still authoritative for tracker **semantics** (message meanings, state, life cycle); its wire-encoding section is superseded by v0.2. |

~~Minor: `AGENTS.md` points at `docs/diagrams/uc-*.puml`. No such directory —
the diagrams are at `docs/uc-*.puml`.~~ **Fixed**; precedence entry 6 now names
the real path. Note that `AGENTS.md`'s layout section still describes the tree
as it was planned, not as it is: `docs/adr/` exists and is not listed there
either.

## 2. State of the tree

Gate passes. What exists and works:

- `cmd/trac` — tracker, spec-complete against v0.2.
- `internal/discovery` — tracker client + keep-alive loop, wired into the daemon.
- `internal/daemon/watcher.go` — cache watcher, wired, read-only. Skips symlinks
  and strips `~hash10` (§4.1).
- `internal/config` — load / validate / generate-to-stdout. Two listen
  addresses, `facade_addr` loopback-enforced, plus `repo_db_dir`. No writer,
  by design.
- `internal/daemon/repodb.go` — `Repositories`, the repository database reader
  (§5.2). Reads every catalogue under `repo_db_dir`, read-only, as an in-memory
  snapshot. Wired into both the watcher and the facade.
- `internal/daemon/facade.go` — the mirror facade handler. Path rule handles
  `All/Hashed/` and `~hash10`; spools through `temp_dir`; skips and marks
  blacklisted peers via `peer.FetchFirst`. **Mounted on `facade_addr`.**
- `internal/peer` + `internal/peerwire` — fetch and seed over the interim binary
  framing. **Not mounted**, and being replaced (§3.2).
- `internal/peer/blacklist.go` — the local peer blacklist (§5.5). In-memory, no
  expiry, not persisted; one list per `Facade`.

What does not exist at all: the **seed half, and it is worse than "not wired"**.
There is no production `PackageSource` implementation anywhere in the tree — the
only three implementors are two test fakes and `cmd/demo`'s in-memory store, and
nothing reads the pkg cache for file *contents* (the watcher reads it for names
only). `peer.Server` is constructed in exactly one place, `cmd/demo/main.go`.

So **§5.3 is a build, not just a migration**: on top of the wire change its
migration table describes, it has to write the cache-backed seeder that has
never existed. Budget for that. The facade half of §5.4 is done; the peer half
needs both.

~~**The daemon should now do something useful against a real FreeBSD host, and
nobody has checked.**~~ **Checked, 2026-08-08 — see §7.** The facade model works
against real pkg: a stand-in that proxied the signed catalogue was accepted as a
genuine repository (37,789 packages, `signature_type: fingerprints` intact) and a
real `pkg install` through it succeeded end to end.

**But §7.1 came back false**, and it is the assumption the failure paths rest
on: pkg does not fall through from a `404` to another *repository*, only between
*mirrors of one repository*, and no mirror mechanism can currently name a
loopback daemon. Read §7 before planning any facade or UC-02 work — the
status-code design may have to change, and that is an owner ruling, not a
refactor.

## 3. Decided by the owner — implement, do not re-litigate

### 3.1 Config schema — **DONE**

Implemented and written into UC-01, the diagram and `cmd/jmj/README.md`. Work
log: `docs/logs/claude-config-schema.md`. Kept here as the record of what was
decided; nothing below is outstanding except where marked.

- Replace `listen_addr` with **two** fields:
  - `facade_addr` — where pkg reaches the daemon. Loopback.
  - `serving_addr` — where peers reach the daemon. Public; its port is what
    gets announced to the tracker as `servingPort`.
- **Loopback on `facade_addr` is mandatory.** The daemon refuses to start if it
  is not a loopback address. This is not advisory.
- Rename `buffer_dir` → `temp_dir`, default `os.TempDir()`, and **actually use
  it** — `os.CreateTemp` per download.
- Drop the `/ping` handler from the daemon's mux in `startHTTPServerLocked`. It
  is an invented health endpoint that appears in no spec.

Fallout, all fixed: `docs/uc-01.puml`, the UC-01 row in
`docs/use-case-descriptions.md`, `cmd/jmj/README.md`, `internal/config` and its
tests, and `servingPort(listenAddr)` in `daemon.go` — the provisional
derivation is gone and the port comes off `serving_addr` via
`DaemonConfig.ServingPort()`.

Two things decided while implementing, either of which the owner may want to
revisit:

- **A config carrying `listen_addr` or `buffer_dir` is a startup error**, not a
  silent upgrade. `encoding/json` drops unknown keys, so the alternative was a
  daemon coming up on default ports with the user's setting discarded. The file
  is left alone rather than moved to `.bak` — it is wrong, not corrupt. UC-01
  error state 4.
- **`temp_dir` is spooled through by the facade, and the round-trip buys
  nothing yet.** `FetchFromPeer` still returns a `[]byte`, so the package is
  already resident before the spool starts; this pays off only when §5.3 makes
  the fetch streaming. Implemented rather than deferred so the field is not
  merely validated-and-unused. Reverting it is one commit if you would rather
  not pay the I/O until then.

Context if you want it: the owner asked why a cache directory under
`~/.cache/jmj` was needed at all when `/tmp` per package would do. It is not
needed. The buffer exists only because verification needs the whole file before
any byte may reach pkg — it is not a store.

### 3.2 Peer transfer wire

Decided and specified in `docs/peer-transfer-spec-v0.2.md`. Not implemented.
That document has a migration table and a definition of done; work to them.

The three decisions behind it, so you do not reopen them: HTTP over TCP (not
chunked binary framing); a peer-private `/pkg/<name-version>` namespace,
deliberately *unlike* the facade's `…/All/<name-version>.pkg`; and a fuzz target
aimed at the peer server's HTTP surface end to end.

## 4. Blocked — needs an owner decision before you write code

**Nothing in §4 is blocked any more.** All three are retained for their
reasoning and evidence, which the implementing work still needs.

### 4.1 Cache and path layout — **RATIFIED AND IMPLEMENTED**

Moved out of "blocked": the owner ratified the proposed rules as written, and
they are implemented. Kept here in full because the measurements are the
evidence behind the rules and the residual risk is still real.

The rules as shipped: the facade locates `All` anywhere in the path (last one
wins), tolerates an optional `Hashed/` after it, and strips a trailing
`~[0-9a-f]{10}` from the stem; the watcher skips symlinks and strips the same
suffix. The strip lives in `parsePackageName`, shared by both surfaces — that
is a filename rule, not a fourth path grammar, and the three *directory*
grammars stay separate as §8 requires.

**Residual risk, unchanged:** one host, one repository, one ABI. §7.5 and §7.6
still bear on this and are still unanswered. The suffix match is exactly ten
lowercase hex digits rather than anything looser, because a tilde is legal in a
pkg version and a permissive rule would silently truncate real versions into
identifiers no peer holds.

The measurements follow.

**(a) The facade rejects real mirror paths.** `pkg -d fetch -y -o /tmp/jmjprobe
indexinfo` requested `All/Hashed/indexinfo-0.3.1_1~ae9dce33aa.pkg`. Measured:

```
/…/All/Hashed/indexinfo-0.3.1_1~ae9dce33aa.pkg  ->  nameVersion="",                isPackage=false
/…/All/indexinfo-0.3.1_1.pkg                    ->  nameVersion="indexinfo-0.3.1_1", isPackage=true
```

`packageRequest` requires `All` to be the **second-to-last** segment, so the
`Hashed/` subdirectory fails rule 1 and the request is classified as metadata.
The `~hash10` suffix would break the name-version even if it passed.

**(b) The cache is flat and full of symlinks.** In `/var/cache/pkg`:

```
indexinfo-0.3.1_1.pkg -> indexinfo-0.3.1_1~ae9dce33aa.pkg     (lstat 32 bytes; target 5905)
```

`parsePackageName("indexinfo-0.3.1_1~ae9dce33aa.pkg")` yields
`"indexinfo-0.3.1_1~ae9dce33aa"` — a string no peer will ever ask for, announced
to the tracker as if it were real. And `filepath.Walk` uses `Lstat`, so a
symlink's size is the link's, which fails any `pkgsize` sanity check.

The rules above were proposed here by the implementing agent and **ratified by
the owner unchanged**. The `~hash10` value is the first 10 characters of
`cksum`, and `path` in the repo DB is
`All/Hashed/<name>-<version>~<hash10>.pkg`.

The question that rode along — write defensively for layouts we have not seen,
or strictly to what pkg 2.7.5 demonstrably does — was answered by ratifying the
rules as proposed: they follow what was measured. Widening them later is cheap;
just do not do it on speculation.

### 4.2 Serving-side concurrency — **DECIDED AND WRITTEN UP AS ADR-002. Implement with §5.3.**

`docs/adr/adr-002-serving-side-concurrency.md` is now the authority for this;
it is what gives §5.3 the ground-rule-1 mandate the peer spec's silence
withheld. What follows is the reasoning that produced it, kept because the
argument is the evidence.

The owner states hostile peers are expected. That settles it, and it reverses
the recommendation I first gave, so the reasoning is worth keeping.

The argument against a limit is real: the peer spec mandates an unbounded body
transfer and AGENTS.md forbids stall detectors, so N slow peers hold all N slots
with nothing able to reclaim them — a lockout that does not exist without the
limit. What that argument misses is **blast radius**. The fd budget is
per-process. With no limit an attacker exhausting descriptors does not just stop
seeding: it breaks the facade's outbound fetches and the tracker keep-alive too,
so this daemon stops installing packages and drops out of the swarm. With a
limit the damage is confined to seeding. Under a hostile model the limit
confines, so take it.

**Mechanism: a non-blocking semaphore in the handler, `503` when full.** Not a
listener-level limit — that queues excess connections invisibly until the
requester's 5s dial timeout fires, so exhaustion stalls the swarm instead of one
peer. A `503` is legible in both peers' logs and the requester already treats any
non-200 as "try next peer", so load spills to another holder with no requester
change. Two ways to get it wrong: `503` must **not** trigger the UC-06 §5b
re-announce (that attaches to `404` — we do still hold the package), and do not
send `Retry-After`.

**Default `0` = unlimited**, configurable. AGENTS.md asks for a real observed
problem before a control of this family; the hostile-peer expectation justifies
the mechanism, not a specific number nobody has measured.

Implement **with §5.3, not before**: there is no `503` on the `peerwire`
framing, which that work deletes.

**Ruled: adopt the per-remote-IP cap too.** A *global* limit still lets one
hostile IP hold every slot, because nothing reclaims them. The cap is the second
semaphore in the same handler, keyed on the host half of `r.RemoteAddr` — never
a header — and it also defaults to `0` = unlimited. The NAT objection is real but
bounded: a `503` spills the requester to the next of its `MAX_PEERS = 3` holders
and then to pkg's own mirror fallback, so a capped-out legitimate requester gets
a slower install, not a broken one. Rationale, rejected alternatives and the
distributed-attacker limitation are in ADR-002.

Note before tuning anything: concurrency is not today's binding constraint. The
current seeder is byte-slice based and copies the payload twice, so one request
for the 2.83 GiB package OOMs a 1 GiB host whatever the limit is. §5.3 is the
fix.

### 4.3 Whether `PackageHashes` and `RepositoryDatabase` merge — **DECIDED AND DONE**

They compose rather than collapse. `Repository` (in `repository.go`) embeds both
narrow interfaces; the facade holds the composite, the watcher keeps taking the
size-only `RepositoryDatabase`.

The reason it is not one struct-returning `Lookup`: that would protect the peer
spec's "hash and size arrive together" invariant at the cost of a different
guarantee. `SanityFilter`'s signature currently *proves* the announce path
cannot hash, because it can only ask for a size. A struct with a `Hash` field
the watcher is trusted to ignore trades a type-level guarantee for a comment.
Composing keeps both, and `watcher.go` did not change at all.

Decided alongside it: the reader is a **snapshot**, not a live query — neither
half returns an `error`, and that is only honest if a lookup cannot fail.
Measured 38,074 packages in **6.3 MB**. And repository identity is **hidden**:
no consumer has a use for it, and `name-version` was measured collision-free
across both repositories.

**Both riders are now RATIFIED**, each with one correction to the diagnostics:

1. **Cross-repository collisions** resolve to the first in sorted path order,
   deterministically. Measured zero collisions. The alternatives were
   refuse-to-start and accept-and-log; first-wins won because the downside is
   bounded — UC-02 step 10 has pkg re-verify the bytes we hand over, so a wrong
   pick degrades to a failed install, never a corrupt one — whereas refusing to
   start lets one misconfigured third-party repository take the daemon down.
   The choice cannot be delegated to pkg: the facade needs an expected hash
   *before* it fetches and there is no "ask pkg" step, and the swarm has no
   repository dimension at all (bare `name-version` at the tracker,
   `/pkg/<name-version>` at the peer). **Correction applied:** the log now names
   *which* name-versions collided, capped with an "and N more" tail, because the
   one consequence pkg's re-verification does not cover is that a wrong row
   makes us blacklist an *honest* peer for our own bad data, and the names are
   the only thread back to that.
2. **Malformed rows are dropped** (`cksum` not 64 lowercase hex, or `pkgsize`
   not positive). This edges toward the defensiveness §4.1 warned against, and
   is ratified anyway because the failure is asymmetric: a malformed expected
   hash cannot match any bytes, so the fetch path would blacklist an *honest*
   peer for our own bad data. None of the 38,074 real rows was dropped, so this
   is a non-issue in practice. **Correction applied:** the two causes are
   counted and reported separately and each names its rows. The counter was
   incremented by `!isHexSHA256(cksum) || pkgsize <= 0` while the message said
   only "malformed cksum", so a `pkgsize` problem was diagnosed as a checksum
   problem.

## 5. Unblocked work, in order

### 5.1 Config schema (§3.1) — **DONE**
All three acceptance criteria met: a non-loopback `facade_addr` refuses to
start with a message naming the field, `-generate-config` still touches no
filesystem, and `temp_dir` has a real consumer (with the caveat in §3.1).

### 5.2 Repository database reader — **DONE**

`internal/daemon/repodb.go`. Work log: `docs/logs/claude-repo-db-reader.md`.
Driver: `modernc.org/sqlite` (pure Go, so the gate needs no C toolchain and
cross-compiling to FreeBSD stays a `GOOS` setting). Opened `mode=ro` plus
`query_only`, so the read-only constraint on pkg's signed catalogue is enforced
by the driver rather than by our discipline.

**It reads a directory, not a file.** The reference host has *two* repositories
— `FreeBSD-ports` (37,835 rows) and `FreeBSD-ports-kmods` (239) — and every
document before this one said "the repository database", singular. New config
key `repo_db_dir`, default `/var/db/pkg/repos`.

**Still open:** nothing triggers `Reload()`. `pkg update` rewrites these files,
so a long-running daemon goes stale and starts answering 404 for packages added
since startup. `Reload()` exists and is tested; wiring a trigger (a watch on
`repo_db_dir`, or a periodic reload) is follow-up.

The schema, for reference:

```sql
-- /var/db/pkg/repos/FreeBSD-ports/db, table "packages"
name TEXT, version TEXT, pkgsize INTEGER, cksum TEXT, path TEXT, ...
```

- `cksum` is the **lowercase hex SHA-256 of the .pkg file**. All 37,835 rows on
  the inspected host are 64 lowercase hex; verified byte-for-byte against three
  cached files. This ratifies the assumption isolated in `peer/fetch.go` and
  facade open question 2.
- `pkgsize` is the **file size in bytes**, verified. Do not confuse it with
  `flatsize`, which is the installed size and 2–20× larger.
- `path` is `All/Hashed/<name>-<version>~<hash10>.pkg`, `~hash10` being the
  first 10 characters of `cksum`. Relevant to §4.1.
- The `meta` table gives `{"version":2,"packing_format":"tzst",…}`, which
  confirms the `.pkg` extension the watcher assumes and closes its TODO.

Verified against the real catalogues, not just fixtures: `indexinfo-0.3.1_1`
resolves to hash `ae9dce33aa72…` and size **5905**. The `~ae9dce33aa` suffix
measured in §4.1(a) is the first ten characters of that hash, and 5905 is
exactly the symlink target size measured in §4.1(b) — three independent
measurements from different sessions agreeing.

The `meta` table finding has been applied: the watcher's `.txz`-vs-`.pkg` TODO
is closed and `packageFileExtension` now records the measurement rather than an
assumption.

Practical notes for that host: python3 there has no `_sqlite3` module, and
`/usr/local/bin/sqlite` is 2.8.17 and cannot read SQLite 3 files — but the owner
has since installed `sqlite3`, so use that. Default shell is `/bin/sh`: **no
bashisms, no GNU extensions.**

### 5.3 Peer wire migration (§3.2)
Work to the peer spec's migration table and definition of done. This deletes
`internal/peerwire` and rewrites `peer.Server`, `FetchFromPeer`, `PackageSource`,
`cmd/demo` and both peer test files. It also has to build the cache-backed
seeder that has never existed — see §2, which is the reason this is a build and
not just a migration.

Two things it must carry that are easy to lose:

- The §4.2 `503` semaphores — **both** of them, global and per-remote-IP, per
  ADR-002. Also the `503` row that document says the peer spec still needs.
- The size bound from §5.5, as real code: `io.LimitReader(body,
  expectedSize+1)` plus the `Content-Length` check. Neither can exist while the
  fetch path returns a `[]byte`.

**Do not delete `internal/peerwire` before that bound is in place.**
`MaxPayload` (`wire.go:24`, enforced at `:53`) is today the *only* length check
on the fetch path — `FetchFromPeer` reads through `peerwire.ReadMessage` — so
removing the package first leaves the fetch loop with nothing between it and a
hostile peer streaming unbounded bytes. This is about ordering within §5.3, not
a reprieve: `MaxPayload` is a 64 MiB global cap of exactly the kind §8 forbids,
and it goes when its replacement lands, in the same change.

### 5.4 Mount the facade and the seed server — **facade half DONE**

The facade is mounted at the root of `facade_addr`, with `config.TempDir`
passed in. Startup order is now catalogue → discovery → listener, because the
facade needs both; failing to read the catalogue is fatal to startup, matching
`Facade.Check`.

**The seed server half remains** and belongs to §5.3: `peer.Server` still speaks
the `peerwire` framing, not HTTP, so there is nothing worth putting on
`serving_addr` until that migration lands.

Trap for whoever touches this: a nil `*Repositories` assigned into an interface
field is a **non-nil interface holding a nil pointer**, so every `== nil` check
downstream passes and the first call panics. It silently defeated `Facade.Check`
— the daemon listened, then would have panicked on the first package request.
Both wiring sites go through `Daemon.repository()` for this reason, and
`TestStartHTTPServerRefusesWithoutARepositoryDatabase` is the regression test.

### 5.5 Local peer blacklist — **done**
Landed via `claude/branch-handoff-j5u55i` (`baa515a`), merged into the §5.1/§4.1
work rather than rebased onto it. `internal/peer/blacklist.go` plus
`peer.FetchFirst`, which both `Download` and the facade now call instead of
keeping two copies of the peer loop. Local only, never reported to the tracker.
Work log: `docs/logs/claude-peer-blacklist.md`.

The three positions left deliberately undecided are **now ratified as the code
already had them**: entries have **no expiry**, are **not persisted** across
restarts, and a peer is blacklisted **for everything**, not per package —
corrupt bytes are evidence about the peer, not about one file.

**Culling is by restart, and that is the whole mechanism.** The list is
in-memory and unpersisted, so a restart clears it completely. There is no
`Unblock` and none is planned: something would have to call it, and an admin
surface is in no spec. What the ruling did require is that the log show what a
restart would clear, so `download.go` now reports the resulting list on every
block — which finally gives `Blacklist.Addrs()` the caller it was written for.

The wrinkle about the unreachable size branch is **resolved by ruling, not left
dangling**. UC-02 used to say "a size or hash mismatch blacklists" while the
fetch path only ever raised `ErrHashMismatch`. The owner's ruling keeps it that
way: `pkgsize` is a **transfer bound, not a verdict**. A `Content-Length` that
disagrees, or a body that overruns, abandons the peer and moves to the next
holder *without* blacklisting — wrong-length bytes fail the hash anyway if read
to completion, so a size verdict is a second route to the same conclusion. Only
`ErrHashMismatch` blacklists. A `404` still does not: it means the peer no
longer holds the file, not that it lied. UC-02 steps 9/9c/11c, its assumptions
cell, `uc-02.puml` and the peer spec's size-bound section now state one rule.

### 5.6 The other §4.1 half nobody has checked — **CLOSED**
~~The facade and the watcher now agree on `~hash10`, but §7.5 below — whether
the cache after a real `pkg install` matches the `All/Hashed/` layout seen from
a `pkg fetch -o` probe — is still unverified.~~

**Verified 2026-08-08.** A real `pkg install` requests the same
`All/Hashed/<name>-<version>~<hash10>.pkg` shape the `pkg fetch -o` probe did,
and the cache it writes stays flat — no `All/` or `Hashed/` directories, and the
`~hash10` symlink pattern of §4.1(b). The observations are consistent with each
other, not merely each consistent with the rules. See §7.5 and the work log.

## 6. Known defects

- ~~`peer.Server.Serve` hot-spins on a closed listener.~~ **Fixed** with §5.5:
  `Serve` returns on a permanent `Accept` error and backs off 5ms→1s on a
  temporary one, the shape `net/http.Server.Serve` uses.
- ~~`facade_test.go` leaks a listener to work around that bug.~~ **Fixed**
  alongside it; the helper now closes its listener via `t.Cleanup`.
- `cmd/demo` depends on `peerwire` and on `PackageSource.Get` returning
  `[]byte`. It must be rewritten in §5.3, not deleted.
- **The daemon announces a serving port nothing listens on.** `daemon.go`
  announces `config.ServingPort()` to the tracker and the keep-alive announces
  the whole cache, but no seed server is mounted and none can be until §5.3
  builds one (§2). Every peer that acts on our tracker entry therefore dials
  `serving_addr` and gets connection-refused. That correctly does not blacklist
  us — a dial failure never does — so the cost is one wasted attempt per peer,
  and it is paid by the rest of the swarm rather than by this daemon. Worth
  knowing before anyone reads a trial's peer logs and concludes the tracker is
  broken.

## 7. Information we do not have — get it before deciding

Everything here needs the FreeBSD host. The owner has granted SSH access
(`root@45.76.163.52`).

**Recon is done; the experiment is not.** Read-only commands only — nothing was
written to the host. Work log: `docs/logs/claude-pkg-mirror-verification.md`,
which carries the probe-server script and the repo configs ready to run. **One
run of that harness answers items 1–5**: it proxies signed metadata from a real
mirror so pkg treats it as a genuine repository, intercepts only `All/*.pkg`,
and logs every request with headers — so items 3 (`HEAD`/`Range`) and 5 (cache
layout) fall out of the log and the post-install cache at no extra cost. What
is now measured:

| Fact | Value |
|---|---|
| OS / pkg | `FreeBSD 15.1-RELEASE-p1 amd64`, pkg **2.7.5** — the same pkg behind every §4.1 measurement |
| ABI | `FreeBSD:15:amd64`, osversion `1501000` |
| `/usr/local/etc/pkg/repos/` | **does not exist** |
| Tools | `python3` 3.12.13, `sqlite3`, `fetch`, `nc`. **No `curl`** — write recipes with `fetch -qo -`. |
| `/var/cache/pkg` | 32 entries, 20G free |

**The experiment has been RUN (2026-08-08) and §7.1–§7.5 are all answered.**
The host was returned to baseline, verified. Full detail and controls in the
work log; the summary is below, and **§7.1's answer changes the architecture.**

> **pkg falls through on a non-200 between *mirrors of one repository*, never
> between *repositories*.** `man pkg-repository` on HTTP mirror lists: "Mirrors
> are tried in the order listed **until a download succeeds**." On multiple
> repositories it says only that pkg will **search** them in `PRIORITY` order —
> solve-time selection, not fetch-time retry.
>
> jmj is designed as a repository that answers `404` and expects pkg to go
> elsewhere. **It will not.** Measured with `jmjprobe` at priority 100 and stock
> `FreeBSD-ports` at priority 0, both catalogues at 37,789 rows, the target
> package confirmed present in the other repository: `404`, `503` and
> connection-refused all fail the install with exit 1 and no retry. The
> connection-refused case was run with **the probe process killed**, so no code
> of ours was in the fetch path — the finding is about pkg's model, not the
> harness.

**Consequence, which needs an owner ruling and is NOT decided:** the facade may
have to fall back to the real mirror *server-side* — becoming a
proxy-with-fallback rather than a mirror-that-declines. The harness worked
precisely because it proxied upstream instead of 404ing. That touches every
UC-02 failure path, all of UC-07, and the facade spec's status-code table. See
"What this costs the design" in the work log. Note the `404` design is *correct*
under mirror-list semantics — it is only wrong under repository semantics, so
"is the pkg segfault below fixable?" is a real input to the decision.

1. **Does pkg actually fall through to the next mirror on a non-200?** This is
   *the* load-bearing assumption of the entire design — every failure path in
   UC-02 and all of UC-07 depend on it — and it has **never been verified**.
   UC-07's assumptions say the integration smoke test must confirm it. If it is
   false, the architecture changes. Do this before building more on top of it.

   **ANSWERED — and the answer is the bad one.** Between mirrors, yes;
   between repositories, no. jmj depends on the latter. See the block above;
   full controls in the work log. The ambiguity this item contained turned out
   to *be* the answer, which is why it was worth stopping on rather than
   picking a reading.
2. **How is mirror ordering configured?** Getting the daemon *first* and a real
   mirror second. UC-07 says the same smoke test settles it. We have no
   confirmed `pkg.conf` / `repos/*.conf` recipe, and without one the daemon
   cannot be exercised in situ at all.

   **Sharpened by recon.** There is no recipe to discover — `/usr/local/etc/pkg/repos/`
   does not exist on the host, so this has to be *derived*, and whatever is
   derived is the first such config the project has ever had. The stock repo is
   `mirror_type: "srv"` over `pkg+https://`, with `_https._tcp.pkg.freebsd.org`
   resolving to a single SRV record. Three candidate mechanisms, with very
   different consequences for jmj:

   **ANSWERED: none of the three currently delivers it.**

   1. **`mirror_type: "srv"`** — real, but ordering comes from DNS SRV records,
      so naming a loopback daemon needs a zone you control. Not reachable from
      `repos/*.conf` alone. Untested, for want of a zone to edit.
   2. **`mirror_type: "http"`** — the documented mechanism and the one that fits
      jmj exactly. **It segfaults pkg 2.7.5.** `pkg update` dies with
      `Segmentation fault (core dumped)`, or with `Sandboxed process …
      terminated abnormally by signal: 11` + `pkg: No signature found` — the
      crash is in the sandboxed signature-verification child. **Not our bug:**
      the list was `URL: ` + whitespace + one URL, exactly as
      `man pkg-repository` specifies, and it reproduces with a list naming only
      the real upstream mirror, with our probe serving nothing but that one text
      document.
   3. **Two repositories with `priority`.** Configures cleanly; gives selection
      without fall-through. Useless for this purpose (item 1).

   So there is a working recipe for *being* the mirror (single repo,
   `mirror_type: none`, proxying the signed catalogue — this is what the harness
   did, and a real `pkg install` through it succeeded end to end). There is no
   working recipe for *being first among several*.
3. **Does pkg issue `HEAD` or `Range` against mirrors?** ~~Facade open questions
   1 and 5.~~ **ANSWERED: neither.** Every logged request — catalogue refresh,
   `pkg fetch`, real `pkg install` — was a plain `GET`. Zero `Range`, zero
   `HEAD`. User-agents `pkg/2.7.5` and `fetch libfetch/2.0`. **Scope:** all
   observed transfers were small and none was interrupted; resume-after-
   interrupt was not tested and `Range` could plausibly appear there. Facade
   open questions 1 and 5 are answered for the normal path only.
4. **What does pkg do with a `200` whose body fails its own checksum?**
   **ANSWERED: it detects it and then has nowhere to go.** A `200` with correct
   `Content-Length` and garbage bytes gives `pkg: <pkg> failed checksum from
   repository`, exit 1, nothing landed, and — as in item 1 — **no attempt
   against the other repository**. So a facade that serves wrong bytes is a
   broken install, not a degraded one.
5. **Cache layout after a real `pkg install`.** **ANSWERED: consistent, and the
   cache stays flat.** A real install requested
   `All/Hashed/<name>-<version>~<hash10>.pkg` — the same shape §4.1(a) measured
   from a `pkg fetch -o` probe, so the two observations agree, which is exactly
   what §5.6 said nothing had established. `find /var/cache/pkg -type d` returns
   only `/var/cache/pkg`: nothing writes `All/` or `Hashed/` into the cache. The
   resulting entry is the §4.1(b) symlink pattern
   (`…_1.pkg -> …_1~49f94c8aa7.pkg`, 757 B target). **§5.6 is closed too.**
6. **Is `cksum` ever not sha256-hex?** ~~One repository, one ABI.~~ **Largely
   settled:** checked across **both** repositories on the host — 38,074 rows,
   **zero** that are not 64 lowercase hex. The schema also declares
   `cksum TEXT NOT NULL` and `pkgsize INTEGER NOT NULL`, so the two facts are
   guaranteed together by the database rather than by convention. Residual risk
   is now one *host* rather than one repository; `pkg_format_version` is NULL in
   every row and `manifestdigest` remains uninvestigated. The reader drops any
   row that fails the format check, so a violation degrades to "that package is
   not served" rather than to a bad verification.
7. **Where does the tracker run for a real trial?** Deployment is unspecified.
   The daemon defaults to `http://127.0.0.1:8080`, which is fine for tests and
   useless for two machines. Also worth confirming the accepted "one daemon per
   public IP" limitation does not bite the trial topology.

## 8. Traps

Things previous agents got wrong, or were explicitly told not to do:

- **Do not reintroduce a global package size cap.** The one that existed blocked
  1.30% of the repository outright, including llvm, rust, chromium and
  libreoffice. The bound is the exact expected size from the repo DB.
- **Do not add stall detectors, minimum-throughput rules or transfer
  deadlines.** A slow peer is out of scope exactly as a slow mirror is.
- **Do not unify the three path grammars.** Tracker, facade and peer wire are
  separate surfaces and the peer namespace differs from the facade's on purpose.
- **Do not add a config writer**, or any permission handling to
  `-generate-config`. It prints to stdout and the shell does the writing. There
  is no `config.Save` and there is not meant to be.
- **Do not create directories in, or write to, the pkg cache or the repository
  database.** The watcher used to `MkdirAll` the cache dir; that was a hard
  constraint violation and it is fixed. Do not reinstate it.
- **Do not read the existing config as a merge base in `-generate-config`.** The
  shell truncates the redirect target before jmj starts, so there is nothing to
  merge. `docs/uc-01.puml`'s legend explains this at length.
- **PlantUML:** square brackets in an `alt`/`else` label are parsed as a link and
  eat the first word; angle brackets in a note body are parsed as markup. Render
  to PNG and *look at it* before committing a diagram.
- **Do not claim a fact about a system you have not inspected.** An earlier
  session asserted a "flat GhostBSD-style cache layout" that was an invention;
  the owner caught it. Every empirical claim in this file traces to a measurement
  on a named host.
