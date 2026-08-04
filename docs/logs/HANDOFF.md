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

Current branch: `claude/branch-handoff-j5u55i`, ahead of `main` by the peer
blacklist (§5.5) and main merged back into it.

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
| `docs/uc-01.puml`, `cmd/jmj/README.md` | Current as of the two-address config. |

### Stale — do not act on these without reading §3.1 first

| Document | What is wrong with it |
|---|---|
| `docs/logs/elroy-uc1-config.md` §"Decision 3" | **Actively misleading**, and now doubly so. It justifies `buffer_dir` as needing to "persist across reboots". That is wrong: the daemon has no store of its own and serves straight from the pkg cache, and the buffer is per-request and ephemeral. The field it describes no longer exists either — its settings table still lists `listen_addr` and `buffer_dir`. Left unedited because it is another author's work log; read it as history only. |
| `docs/protocol-spec-v0.1.md` | Historical. Still authoritative for tracker **semantics** (message meanings, state, life cycle); its wire-encoding section is superseded by v0.2. |

Minor: `AGENTS.md` points at `docs/diagrams/uc-*.puml`. No such directory —
the diagrams are at `docs/uc-*.puml`. Fix it if you are touching `AGENTS.md`
anyway; it is not worth a commit of its own.

## 2. State of the tree

Gate passes. What exists and works:

- `cmd/trac` — tracker, spec-complete against v0.2.
- `internal/discovery` — tracker client + keep-alive loop, wired into the daemon.
- `internal/daemon/watcher.go` — cache watcher, wired, read-only. Skips symlinks
  and strips `~hash10` (§4.1).
- `internal/config` — load / validate / generate-to-stdout. Two listen
  addresses, `facade_addr` loopback-enforced. No writer, by design.
- `internal/daemon/facade.go` — the mirror facade handler. Path rule handles
  `All/Hashed/` and `~hash10`; spools through `temp_dir`; skips and marks
  blacklisted peers via `peer.FetchFirst`. **Not mounted.**
- `internal/peer` + `internal/peerwire` — fetch and seed over the interim binary
  framing. **Not mounted**, and being replaced (§3.2).
- `internal/peer/blacklist.go` — the local peer blacklist (§5.5). In-memory, no
  expiry, not persisted; one list per `Facade`.

What does not exist at all: a repository-database reader, and any wiring that
puts the facade or the seed server on a port.

**The daemon still does nothing useful against a real FreeBSD host**, but for
one reason now rather than two: there is no repo DB reader (§5.2), so `Hashes`
is `nil` and the facade 404s everything. The §4.1 path bugs are fixed.

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

§4.1 is retained but no longer blocked; §4.2 and §4.3 are the live ones.

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

### 4.2 Serving-side concurrency

Nothing specifies how many simultaneous seeds a daemon accepts. Constant-memory
serving makes this far less dangerous than it was, but sockets and file handles
are still finite. No limit was invented. Ask before adding one.

### 4.3 Whether `PackageHashes` and `RepositoryDatabase` merge

Two interfaces, two views of the same repo DB row. The peer wire spec depends on
hash and size always arriving together, which strengthens the case for one
interface returning both. Not decided. Recorded in the peer spec's *Deliberately
unspecified* table. Nothing is blocked on it, but §5.2 is the natural moment.

## 5. Unblocked work, in order

### 5.1 Config schema (§3.1) — **DONE**
All three acceptance criteria met: a non-loopback `facade_addr` refuses to
start with a message naming the field, `-generate-config` still touches no
filesystem, and `temp_dir` has a real consumer (with the caveat in §3.1).

### 5.2 Repository database reader — **the biggest unblocked win, and now the only thing between the daemon and a real end-to-end fetch**

The facade answers `404` to every request because `Hashes` is `nil`, and
`SanityFilter` cannot check sizes. Both interfaces exist and neither has an
implementation. The schema is fully known — this was established over SSH and is
no longer guesswork:

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

Open it **read-only**. It is pkg's file and the hard constraints forbid writing
to it. Acceptance: the facade serves a real package end to end on the FreeBSD
host. §4.1 is no longer a precondition — it is done — so this plus §5.4 is the
whole remaining distance to that.

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
`cmd/demo` and both peer test files.

### 5.4 Mount the facade and the seed server
**Now blocked on §5.2 only.** §5.1 delivered the addresses, so `facade_addr`
and `serving_addr` both exist and the loopback rule is enforced; what remains
is `Facade.Check` refusing to start without a repository database. The `TODO`
in `startHTTPServerLocked` says as much. Facade on `facade_addr`, seed server
on `serving_addr`. Remember to pass `config.TempDir` into `Facade.TempDir` when
you wire it — nothing constructs a `Facade` today, so the field is set by no
caller yet.

### 5.5 Local peer blacklist — **done**
Landed via `claude/branch-handoff-j5u55i` (`baa515a`), merged into the §5.1/§4.1
work rather than rebased onto it. `internal/peer/blacklist.go` plus
`peer.FetchFirst`, which both `Download` and the facade now call instead of
keeping two copies of the peer loop. Local only, never reported to the tracker.
Work log: `docs/logs/claude-peer-blacklist.md`.

Left deliberately undecided, and still open if the owner wants them: entries
have **no expiry** and are **not persisted** across restarts, and a peer is
blacklisted **for everything**, not per package. Each was unasked, so the
literal reading won.

One wrinkle worth a second look: UC-02's refinement says **a size or hash
mismatch blacklists**, but the fetch path only ever raises `ErrHashMismatch` —
there is no separate size check to trip, so "size" is currently unreachable
rather than implemented. A `404` correctly does not blacklist: it means the peer
no longer holds the file, not that it lied.

### 5.6 The other §4.1 half nobody has checked
The facade and the watcher now agree on `~hash10`, but §7.5 below — whether the
cache after a real `pkg install` matches the `All/Hashed/` layout seen from a
`pkg fetch -o` probe — is still unverified. The rules are consistent with both
observations; nothing proves the observations are consistent with each other.

## 6. Known defects

- ~~`peer.Server.Serve` hot-spins on a closed listener.~~ **Fixed** with §5.5:
  `Serve` returns on a permanent `Accept` error and backs off 5ms→1s on a
  temporary one, the shape `net/http.Server.Serve` uses.
- ~~`facade_test.go` leaks a listener to work around that bug.~~ **Fixed**
  alongside it; the helper now closes its listener via `t.Cleanup`.
- `cmd/demo` depends on `peerwire` and on `PackageSource.Get` returning
  `[]byte`. It must be rewritten in §5.3, not deleted.

## 7. Information we do not have — get it before deciding

Everything here needs the FreeBSD host. The owner has granted SSH access
(`root@45.76.163.52`).

1. **Does pkg actually fall through to the next mirror on a non-200?** This is
   *the* load-bearing assumption of the entire design — every failure path in
   UC-02 and all of UC-07 depend on it — and it has **never been verified**.
   UC-07's assumptions say the integration smoke test must confirm it. If it is
   false, the architecture changes. Do this before building more on top of it.
2. **How is mirror ordering configured?** Getting the daemon *first* and a real
   mirror second. UC-07 says the same smoke test settles it. We have no
   confirmed `pkg.conf` / `repos/*.conf` recipe, and without one the daemon
   cannot be exercised in situ at all.
3. **Does pkg issue `HEAD` or `Range` against mirrors?** Facade open questions 1
   and 5. The size objection to answering `HEAD` is gone (§5.2 gives an exact
   size without fetching), so this is now purely a question about pkg's
   behaviour.
4. **What does pkg do with a `200` whose body fails its own checksum?** Retry
   the next mirror, or abort the whole operation? Determines whether a facade
   bug is a degraded experience or a broken one.
5. **Cache layout after a real `pkg install`.** §4.1(b) was observed in
   `/var/cache/pkg`; the `All/Hashed/` path in §4.1(a) came from a
   `pkg fetch -o` probe. Confirm the two are consistent and that nothing writes
   `All/Hashed/` into the cache itself.
6. **Is `cksum` ever not sha256-hex?** One repository, one ABI, 37,835 rows, all
   64-hex. `pkg_format_version` and `manifestdigest` exist in the schema and
   have not been investigated. Either confirm on a second repo/ABI or record the
   residual risk explicitly.
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
