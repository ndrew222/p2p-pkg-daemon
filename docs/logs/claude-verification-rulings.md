# Verification rulings — work log

Four items had been flagged to the owner in `HANDOFF.md` and left unanswered:
§4.3's riders on cross-repository collisions and malformed rows, and §5.5's
leftovers on blacklist expiry, persistence and scope — plus the observation
that UC-02's "a size or hash mismatch blacklists" was unreachable in code,
because the fetch path only ever raises `ErrHashMismatch`. The owner ruled on
all four. This log records the rulings, how they were implemented, and what is
still open.

An earlier revision of this file was a *plan*, written when the rulings had
been obtained but not yet executed. The work has since landed; this is the
work log that replaces it.

## How I approached it

The rulings were already written down and reasoned through, so the work was
execution rather than design. I kept the commits atomic in the order the
dependencies ran, not the order the rulings were numbered:

1. `repodb`: collisions — introduces the capped log helper.
2. `repodb`: malformed rows — reuses it.
3. `peer`: the blacklist's three legs.
4. `docs`: size bounds, hash verifies.
5. `facade`: the stale wiring comment.
6. This log and `HANDOFF.md`.

Collisions came first only because ruling 3 is where the "and N more" cap is
specified, and ruling 4's dropped-row lists need the same cap. Doing it the
other way round would have introduced the helper in the commit that did not
ask for it.

## Ruling 1 — verification blacklists on hash mismatch only

The expected size stays, but only as a **transfer bound**. It is no longer a
verdict.

`pkgsize` was doing two jobs. As a bound it is load-bearing: UC-02's
assumptions argue there is no fixed package size limit precisely because the
exact expected size is a tighter anti-abuse bound than any constant, and §4.2
expects hostile peers — dropping it would leave the fetch path with no defence
against a peer streaming unbounded bytes. As a *verdict* it earns nothing: a
body of the wrong length fails the hash anyway if read to completion, so a
separate size trigger is a second route to the same conclusion and a second
thing to keep consistent across three documents.

So a `Content-Length` that disagrees with the expected size, or a body that
overruns it, **abandons the peer and moves to the next holder**. It does not
blacklist. Only `ErrHashMismatch` blacklists, because only a hash mismatch is
evidence the peer sent bytes that are wrong rather than bytes that are not the
ones we asked for. A `404` continues not to blacklist.

**Changed:** `docs/use-case-descriptions.md` steps 9, 9c, 11c and the UC-02
assumptions cell; one clarifying sentence in `docs/peer-transfer-spec-v0.2.md`
§"The size bound", which already specified the mechanism and needed only the
statement that abandoning is not blacklisting.

**Also changed, and not in the plan:** `docs/uc-02.puml` carried the same
conflation in its `alt` label and in the note beside `markPeerUntrusted`. The
plan listed only the prose. Leaving the diagram saying "only a size or hash
mismatch blacklists" would have left the contradiction exactly where AGENTS.md
says the diagrams are authoritative — where the prose is ambiguous — so it was
corrected in the same commit. Rendered to PNG and inspected before committing,
per the trap in §8 about `alt` labels eating the first word after a square
bracket and angle brackets being parsed as markup. Neither bit.

**No code changed.** The read cap arrives with the streaming rewrite in §5.3,
and `peerwire.MaxPayload` must stay until then or the fetch path is left with
no bound at all.

## Ruling 2 — the blacklist is per-peer, unexpiring, unpersisted, culled by restart

All three of §5.5's "left deliberately undecided" positions are ratified as the
code already had them, and the third leg is now explicit: the key is the **peer
address**, so a peer that serves corrupt bytes is distrusted for *everything*.
Corrupt bytes are evidence about the peer, not about one file.

Culling is by restart. The list is in-memory and unpersisted, so a restart is a
complete cull. No `Unblock` exists and none is planned: something would have to
call it, and an admin surface is in no spec — adding one would trip AGENTS.md
ground rule 2.

What the ruling required beyond recording the decision is that the log show
what a restart would clear. `internal/peer/download.go` already logged the
address and the package on every `Block`, but nothing reported the list as a
whole, and `Blacklist.Addrs()` — written "for logging and tests" — had no
caller. It has one now, rather than a new accessor.

**Changed:** the `UNSPECIFIED` block in `internal/peer/blacklist.go` became a
ratified decision covering all three legs and the cull story; the `Block` log
line in `internal/peer/download.go` now reports the resulting list.

## Ruling 3 — cross-repository collisions: first-wins, and name them

Ratified as implemented — first repository in sorted path order wins,
deterministically — with one addition: log **which** name-versions collided,
not just how many, capped with an "and N more" tail.

**Why this is not pkg's problem to solve**, since it is the first thing the
next reader will ask. pkg does have repository priority (`PRIORITY` in
`repos/*.conf`) and has already chosen a row before it calls the facade, and
UC-02 step 10 has pkg **re-verify** the bytes against that row after we hand
them over. A wrong pick therefore degrades to a failed install, never a corrupt
one.

But the choice cannot be delegated. The facade needs an expected hash *before*
it fetches, so it must pick some row, and there is no "ask pkg" step available.
More fundamentally, the swarm has no repository dimension at all: the tracker
announces bare `name-version` and the peer namespace is `/pkg/<name-version>`
by design (§8), so peers holding a colliding name-version cannot say which of
the two files they have. Recovering repository identity from the request URL
prefix would mean parsing pkg's `repos/*.conf` — a surface in no spec — and
§4.3 already decided repository identity is hidden because no consumer needed
it.

The one consequence pkg's re-verification does not cover: a wrong row makes us
blacklist an **honest** peer for our own bad data. That is why the colliding
names belong in the log, and why first-wins beats refuse-to-start — the
downside is bounded and diagnosable, whereas refusing to start lets one
misconfigured third-party repository take the daemon down.

**Changed:** `internal/daemon/repodb.go` `Reload` collects the colliding keys
and logs them; the `UNRATIFIED` comment is replaced by the ratified rule and
the reasoning above. Tests assert the collision is named and that the cap
elides the surplus rather than merely appending a tail after it.

## Ruling 4 — malformed rows are dropped with a warning

Ratified. This already happened, and the owner notes it is clearly a non-issue
in practice — none of the 38,074 real rows was dropped. The rationale is
unchanged: a malformed expected hash cannot match any bytes, so keeping the row
would blacklist an honest peer for our own bad data, and failing to start would
be worse.

But the warning was wrong in two ways. The counter was incremented by
`!isHexSHA256(cksum) || pkgsize <= 0` while the message reported only
"malformed cksum", so a `pkgsize` problem was diagnosed as a checksum problem
and sent the reader to the wrong column. And the count was aggregate — no
name-version of any dropped row was ever named, leaving nothing to look up.

**Changed:** `loadRepositoryDatabase`'s single `skipped int` became a
`skippedRows` struct with a list per cause. Rows are attributed to the **first**
cause they fail, so the two lists partition the dropped rows rather than
double-counting a row that is malformed twice; this is stated on the type,
because the obvious alternative — counting a row under both causes — makes the
two counts stop summing to the number of rows dropped, which is exactly the
sort of thing a reader assumes without checking.

## Difficulties

**Observing a diagnostic that is only ever logged.** Both rulings 3 and 4 are
about log *content*, and the reader reports collisions and dropped rows nowhere
else — they are diagnostics, not values a caller acts on. Adding return values
just to make them testable would have changed the API to suit the test. Instead
`repodb_test.go` gained a `captureLog` helper that redirects the standard
logger for one test and restores it via `t.Cleanup`. The daemon logs through
package-level `log.Printf` throughout, so this needed no logger plumbing.

**Attributing a doubly-malformed row.** A row can fail both checks at once. The
`switch` attributes each dropped row to its first failing cause, and two
fixtures pin the two halves of that. `zerosize-1.0`, in
`TestOpenRepositoriesSkipsMalformedRows`, has a well-formed `cksum` and a zero
`pkgsize`, and is asserted onto the `pkgsize` line and *not* the `cksum` line —
it catches a fold back into a single `||` reported as one "malformed cksum"
message. It does not catch double-counting, because it only ever fails one
check. That case needs a row failing *both*, and `TestSkipCausesDoNotDoubleCount`
supplies it: `both-1.0`, with a malformed `cksum` and a zero `pkgsize`, asserted
onto the `cksum` line and absent from the `pkgsize` line entirely. Confirmed to
bite by splitting the `switch` into two independent `if`s and watching it fail.

**Naming the rows costs memory, and the obvious saving is a trap.** Rulings 3
and 4 both turned an `int` counter into a `[]string`: `collisions`, and
`skipped.badCksum` / `badPkgSize`. That is `O(1)` → `O(n)` in the number of bad
rows, and the bad case is not small — a third-party repository that shadows
ports wholesale, or a corrupt catalogue whose every row drops. At the reference
host's scale (37,835 rows in the larger repository) the worst case is roughly
**1.4 MB**, held transiently and freed once the log line is formatted: for
`collisions` the entries are map keys from `loaded`, so `append` copies only the
16-byte string header but keeps the backing bytes alive past the point `loaded`
would otherwise be released; for the skip lists each entry is a fresh
`name + "-" + version`. Against a 6.3 MB snapshot that is about +22% at peak,
and nothing against the 1 GiB host UC-02 budgets for. Neither case has been
observed: zero collisions and zero dropped rows on the reference host.

The saving that suggests itself does not work. Since `namesForLog` prints at
most `logNameLimit` names plus a count, it looks like only ten need to be kept.
For the skip lists that would be sound — they are built while ranging
`sql.Rows`, in deterministic order. For `collisions` it is not: that list is
built by ranging `loaded`, a Go map, so iteration order is randomised and
truncating before the sort would print a different ten every run, losing the
determinism the code claims two lines above. Preserving both would need a
bounded min-heap, which is a great deal of machinery for a log line, and
treating the two lists differently is worse than the megabyte. Kept as it is,
deliberately: naming the rows was the ruling, and you cannot name what you did
not keep.

**The diagram was not in the plan.** Covered under ruling 1 above. Rendering it
required fetching PlantUML, since neither it nor a renderer is present in this
environment; the trap in §8 is specific enough that committing an unrendered
diagram edit was not an option.

## Areas of uncertainty

Per AGENTS.md ground rule 2, and the reason this file exists at all:

1. **The §4.2 per-remote-IP cap — raised, unanswered, still blocking §5.3.**
   A *global* concurrency limit still lets one hostile IP hold every slot,
   because nothing reclaims them, and a per-remote-IP cap is in no spec. It was
   put to the owner during the ruling round and is the one thing that round did
   not settle. Not invented here. It does not block anything that landed in
   this work, but it does block §5.3, which is the next substantial item.

2. **Editing files under `docs/`.** AGENTS.md's layout section says
   "`docs/` — specs — agents do not modify these", and rulings 1 and 3 required
   changes to `use-case-descriptions.md`, `peer-transfer-spec-v0.2.md` and
   `uc-02.puml`. This was not an implementer's judgement call: the owner ruled
   on the content and the ruling is only meaningful if the specs it corrects
   are actually corrected — UC-02 asserting a rule the code does not implement
   is the defect being fixed. Flagging it because the line in AGENTS.md is
   unqualified and a future agent should not read these commits as licence to
   edit a spec on its own initiative. If the owner would rather specs changed
   only by their own hand, the four spec hunks are one revert.

3. **The `logNameLimit` value of 10.** Nobody chose this number and no
   measurement bears on it — zero collisions and zero dropped rows were
   observed on the reference host, so the cap has never engaged in practice. It
   is a log-formatting constant with no behavioural consequence, which is why
   it was picked rather than escalated; §4.2's standard ("a real observed
   problem before a control of this family") is about controls that change what
   the daemon does, and this changes only how much of a list is printed. Say so
   if you would rather it were configurable, but it would be a config key
   nobody sets.

## Verification

The gate — `go build ./... && go vet ./... && go test ./...` plus `gofmt` —
passes with no FreeBSD, no `pkg` and no second machine. Specifically:

- `TestOpenRepositoriesSkipsMalformedRows` now asserts the two skip causes are
  counted separately and that a well-formed-`cksum`/bad-`pkgsize` row is
  attributed to `pkgsize` alone. Fixtures only, no real catalogue.
- `TestCollisionResolvesToFirstPathInOrder` asserts the colliding name is
  logged; `TestCollisionLogIsCapped` asserts the "and N more" tail elides the
  surplus.
- `TestFetchFirstBlacklistsCorruptPeer` and
  `TestFetchFirstDoesNotBlacklistUnreachablePeer` already confirmed that
  `ErrHashMismatch` blacklists and nothing else does; both still pass. No new
  size-check test — the read cap lands with §5.3.
- UC-02 steps 9, 9c and 11c, the UC-02 assumptions cell, `uc-02.puml` and the
  peer spec's size-bound section were read together and state one rule, not
  two.
- `docs/uc-02.puml` rendered to PNG and inspected.

`docs/logs/elroy-uc1-config.md` was deliberately **not** edited: §1 of the
handoff says another author's work log is history.

## Out of scope

§5.3 and the cache-backed `PackageSource`; mounting the seed server; the §4.2
per-remote-IP cap; wiring a `Reload()` trigger; and everything in §7 that needs
the FreeBSD host.

## The seed-side gap — reported, not fixed

Found while answering "what is the seed server", and recorded here because it
resizes the next work item. The seed server is `internal/peer.Server`, the
daemon's upload half: a listener on `serving_addr` that answers another
daemon's request for a `name-version` with package bytes read straight from the
pkg cache. It is not the tracker — `cmd/trac` is a separate binary, a directory
service that by explicit design never relays bytes.

The gap: `peer.Server` is constructed in exactly one place in the tree,
`cmd/demo/main.go`, and there is **no production `PackageSource` implementation
at all** — the only implementors are two test fakes and the demo's in-memory
store. Nothing reads the pkg cache for file *contents*; `watcher.go` reads it
only for names. Meanwhile `daemon.go` announces `config.ServingPort()` to the
tracker and the keep-alive announces the whole cache, so this daemon advertises
packages it cannot serve and any peer acting on that entry gets
connection-refused. A dial failure correctly does not blacklist, so the cost is
one wasted attempt per peer, paid by the rest of the swarm.

This makes §5.3 larger than a wire migration: it must **build the cache-backed
seeder that has never existed**. It is recorded in `HANDOFF.md` §2 and §6 as a
known defect rather than fixed here — the fix belongs with §5.3.
