# Work Log: Cache Watcher (UC-05, seeding-registration half)

**Author:** Wei Xian
**Feature:** Cache watcher — the local, read-only half of UC-05 (Announce
Packages / Seeding Registration). Watches the pkg cache directory and
produces a filtered, up-to-date package list on every change.

## Thought process

The UC-05 spec splits cleanly into two halves: a local half (scan the
cache, filter the results) that needs no network access, and a wire half
(ping/announce/requestPackageList against the tracker) that's blocked on
`docs/protocol-spec-v0.2.md`, which doesn't exist yet.

The cache-watcher-triggered path specifically, the diagram states:

> "A new package appeared in the cache. The daemon announces directly — it
> does NOT wait to be asked. Works whether or not the tracker currently
> knows this IP."

This is exactly what `Watch()`/`HandleCacheChanged()` implement: on any
fsnotify event, `scanCache() -> sanityFilter() -> announce()` runs
unconditionally, with no dependency on first hearing back from the
tracker. 

```
Daemon -> Cache : scanCache() (read-only)
Cache --> Daemon : packageList
Daemon -> Daemon : sanityFilter(packageList)
Daemon -> Tracker : announce(listeningPort, fullPackageList)
```

Design decisions I made that trace directly back to specific diagram notes:

- **No hashing, size-based sanity check only.** The diagram's note on
  `sanityFilter(packageList)` reads: "Cheap checks only: valid
  name-version filename, file size matches repo DB entry. No hashing. The
  downloading peer verifies integrity anyway (UC-02)." `SanityFilter()` in
  `watcher.go` implements exactly these two checks and nothing more.
- **`listeningPort` must be an explicit argument, not inferred.** The
  diagram's note on `announce(listeningPort, packageList)` reads: "IP
  comes from the connection's source address; the serving port cannot be
  inferred and must be in the message." This is why `Announcer.Announce`
  (interface) takes `listeningPort` as a parameter rather than assuming
  the network layer can supply it from the connection alone.
- **Always a full re-scan, never an incremental patch.** Both branches of
  the diagram announce a full `packageList` — there's no delta message
  type anywhere in the diagram. `Watch()` deliberately ignores which
  fsnotify event type fired and always does a complete `Scan()`, matching
  this.
- **An empty announced list is a real, distinct outcome, not "nothing to
  do."** The cache-watcher branch's `alt [list empty (e.g. after pkg
  clean)]` shows the tracker explicitly running `drop(IP)` and
  acknowledging "(deregistered)" — a real state transition, not a no-op.
  This is why `HandleCacheChanged()`/`Scan()` always calls `onUpdate`, even
  with an empty slice, rather than short-circuiting when there's nothing
  to report.



## Difficulties and how they were resolved

1. **`parsePackageName` had a real bug**: for a filename with no version
   suffix (e.g. `no-version.pkg`), splitting on the *last* hyphen
   incorrectly split it into `name="no"`, `version="version"`. Fixed by
   requiring the text after the last hyphen to start with a digit before
   treating it as a version; otherwise the whole filename (minus
   extension) is treated as the name with no version. Caught this via a
   failing unit test (`TestParsePackageName`), not by inspection.

## Areas of uncertainty

| Test | UC-05 step(s) / diagram element(s) covered |
|---|---|
| `TestScan` | Prose Step 4 (`scanCache()`) + Step 5 (`sanityFilter()`, size check passes). Diagram: the `scanCache()` → `sanityFilter(packageList)` pair, shared by both `alt` branches. |
| `TestScanEmptyDir` | Precondition ("cache may be empty") + sets up Step 7 / diagram's `[list empty]` branch (both the "nothing retained" ack under the ping branch and the `drop(IP)` under the cache-watcher branch) |
| `TestScan_RejectsSizeMismatch` | Prose Step 5 + diagram note: "file size matches repo DB entry. No hashing." |
| `TestScan_RejectsGarbageFileName` | Prose Step 5 + diagram note: "valid name-version filename" |
| `TestParsePackageName` | Diagram's `packageList` format (name-version identifiers) at the unit level; also underlies UC-02's `IWant(packageName-version)` |
| `TestChangeEvent` | Prose Trigger clause + Step 9 + diagram's second `alt` branch: `"message = announce(list) (cache watcher, unsolicited)"` — "the daemon announces directly — it does NOT wait to be asked" |
| `TestStartAndStop` | Precondition ("daemon is running") + general lifecycle robustness |
| `TestStopIdempotent` | General lifecycle robustness (repeated shutdown must not panic/hang) |
| `TestNew` | Plumbing/construction only — not itself a UC-05 behavioural requirement |

**Not yet covered by any test** :
- Diagram's `ping()` message itself and the `[tracker knows this IP]` /
  `[tracker does not know this IP]` branching (prose Steps 1-3)
- The actual `announce(listeningPort, packageList)` network call and the
  tracker's three-way response (`register`+ack / ack-but-drop / network
  error) — prose Steps 6-7, diagram's inner `alt [list non-empty] / [list
  empty] / [network error mid-stream]`
- Prose Step 8 / diagram note B18: suppressing keep-alive pings while
  nothing is registered. This is explicitly a ping-loop concern, which
  lives in whatever calls this package (`internal/discovery`), not in
  `internal/daemon` itself — see the "Areas of uncertainty" item above
  about who triggers `Scan()`.
- Error State 1 (network error mid-announce, diagram: `logError();
  scheduleRetry()`) and Error State 2 (timeout expiry) — both live on the
  tracker/wire side; the daemon-side retry behaviour will need its own
  test once `Announcer` has a real implementation.



Verified locally on Windows via `go run ./cmd/jmj -id test-peer -addr
localhost:9000 -cachedir <dummy dir>` against a hand-created dummy cache
directory; fsnotify change detection confirmed working.

Not yet verified: behavior against a real FreeBSD `/var/cache/pkg`
directory, and any real network/tracker integration.