# Verification rulings — plan, not yet implemented

**Status: PLANNED. No code or document in this commit has been changed to match
it.** This file records four owner rulings and the change they imply, so the
next session can execute them without re-deriving the reasoning. Delete this
banner and rewrite the file as an ordinary work log once the work lands.

## Why this exists

Four items were flagged to the owner in `HANDOFF.md` and left unanswered: the
§4.3 riders on cross-repository collisions and malformed rows, and the §5.5
leftovers on blacklist expiry, persistence and scope — plus the observation that
UC-02's "a size or hash mismatch blacklists" is unreachable in code, because the
fetch path only ever raises `ErrHashMismatch`. The owner has now ruled on all
four.

A fifth thing surfaced while answering a question about what the "seed server"
is, and is reported here rather than fixed. See the last section.

## The rulings

### 1. Verification blacklists on hash mismatch only

The expected size stays, but only as a **transfer bound**. It is no longer a
verdict.

The distinction matters because `pkgsize` was doing two jobs. As a bound it is
load-bearing: UC-02's assumptions argue there is no fixed package size limit
precisely because the exact expected size is a tighter anti-abuse bound than any
constant, and §4.2 expects hostile peers. Dropping it entirely would leave the
fetch path with no defence against a peer streaming unbounded bytes. As a
*verdict* it earns nothing: a body of the wrong length fails the hash anyway if
read to completion, so a separate size trigger is a second route to the same
conclusion and a second thing to keep consistent across three documents.

So: a `Content-Length` that disagrees with the expected size, or a body that
overruns it, **abandons the peer and moves to the next holder**. It does not
blacklist. Only `ErrHashMismatch` blacklists, because only a hash mismatch is
evidence the peer sent bytes that are wrong rather than bytes that are not the
ones we asked for. A `404` continues not to blacklist — it means the peer no
longer holds the file, not that it lied.

`docs/peer-transfer-spec-v0.2.md` §"The size bound" already specifies exactly
this mechanism (abandon on `Content-Length` mismatch before reading a byte; read
through `io.LimitReader(body, expectedSize+1)`). It needs one sentence saying
that abandoning is not blacklisting. The conflict is confined to UC-02.

**Changes:** `docs/use-case-descriptions.md` steps 9, 9c, 11c and the UC-02
assumptions cell — size bounds, hash verifies, rationale stated inline at 11c
because that is the cell a reader lands on. One clarifying sentence in
`docs/peer-transfer-spec-v0.2.md`. No code: the read cap arrives with the
streaming rewrite in §5.3, and `peerwire.MaxPayload` must stay until then or the
fetch path is left with no bound at all.

### 2. The blacklist is per-peer, unexpiring, unpersisted, culled by restart

All three of §5.5's "left deliberately undecided" positions are ratified as the
code already has them, and the third leg is now explicit: the key is the **peer
address**, so a peer that serves corrupt bytes is distrusted for *everything*.
Corrupt bytes are evidence about the peer, not about one file.

Culling is by restart. The list is in-memory and unpersisted, so a restart is a
complete cull. No `Unblock` exists and none is planned: something would have to
call it, and an admin surface is in no spec — adding one would trip AGENTS.md
ground rule 2.

What the ruling does require is that the log show what a restart would clear.
`internal/peer/download.go` already logs the address and the package on every
`Block`; nothing ever reports the list as a whole, and `Blacklist.Addrs()` —
written "for logging and tests" — has no caller.

**Changes:** rewrite the UNSPECIFIED block in `internal/peer/blacklist.go` as a
ratified decision covering all three legs plus the cull story. Extend the
existing blacklist log line in `internal/peer/download.go` to report the
resulting list size, reusing `Addrs()` rather than adding an accessor.

### 3. Cross-repository collisions: first-wins, and name them

Ratified as implemented — first repository in sorted path order wins,
deterministically — with one addition: log **which** name-versions collided, not
just how many. Cap the list with an "and N more" tail in case a misconfigured
third-party repository shadows ports wholesale.

**Why this is not pkg's problem to solve**, since it is the first thing the next
reader will ask. pkg does have repository priority (`PRIORITY` in
`repos/*.conf`) and has already chosen a row before it calls the facade, and
UC-02 step 10 has pkg **re-verify** the bytes against that row after we hand
them over. A wrong pick therefore degrades to a failed install, never a corrupt
one.

But the choice cannot be delegated. The facade needs an expected hash *before*
it fetches, so it must pick some row, and there is no "ask pkg" step available.
More fundamentally, the swarm has no repository dimension at all: the tracker
announces bare `name-version` and the peer namespace is `/pkg/<name-version>` by
design (§8), so peers holding a colliding name-version cannot say which of the
two files they have. Recovering repository identity from the request URL prefix
would mean parsing pkg's `repos/*.conf` — a surface in no spec — and §4.3
already decided repository identity is hidden because no consumer needed it.

The one consequence pkg's re-verification does not cover: a wrong row makes us
blacklist an **honest** peer for our own bad data. That is the same asymmetric
failure the malformed-row rule guards against, it is why the colliding names
belong in the log, and it is why first-wins beats refuse-to-start — the downside
is bounded and diagnosable, whereas refusing to start lets one misconfigured
third-party repository take the daemon down.

**Changes:** `internal/daemon/repodb.go` `Reload` — collect and log the
colliding keys; delete the `UNRATIFIED` comment and replace it with the ratified
rule and the reasoning above.

### 4. Malformed rows are dropped with a warning

Ratified. This already happens, and the owner notes it is clearly a non-issue in
practice — none of the 38,074 real rows was dropped. But the warning is wrong in
two ways worth correcting while the decision is being recorded.

The counter is incremented by `!isHexSHA256(cksum) || pkgsize <= 0`, while the
message reports only "malformed cksum", so a `pkgsize` problem would be
diagnosed as a checksum problem. And the count is aggregate — no name-version of
any dropped row is ever named.

The existing rationale comment stays: dropping is right because a malformed
expected hash cannot match any bytes, so keeping the row would blacklist an
honest peer for our own bad data. Failing to start would be worse.

**Changes:** `internal/daemon/repodb.go` — split `loadRepositoryDatabase`'s
single `skipped` return into its two causes and report them distinctly. The
daemon logs via package-level `log.Printf` throughout and `repodb.go` already
imports `log`, so no logger plumbing is needed.

## Also in scope

- `internal/daemon/facade.go` says the facade is "Still NOT wired into
  `Daemon.startHTTPServerLocked`". It is, and has been since §5.4. Correct it.
- `HANDOFF.md`: mark §4.3's two riders and §5.5's three leftovers ratified,
  carrying the reasoning above; rewrite §5.5's "one wrinkle worth a second look"
  paragraph, since the unreachable size branch is now resolved by ruling rather
  than left dangling.

Do **not** edit `docs/logs/elroy-uc1-config.md`. §1 says another author's work
log is history.

## The seed-side gap — reported, not fixed

Found while answering "what is the seed server". It is `internal/peer.Server`,
the daemon's upload half: a TCP listener on `serving_addr` that answers another
daemon's request for a `name-version` with the package bytes, read straight from
the pkg cache. It is not the tracker — `cmd/trac` is a separate binary, a
directory service that by explicit design never relays bytes.

The gap: `peer.Server` is constructed in exactly one place in the tree,
`cmd/demo/main.go`, and there is **no production `PackageSource` implementation
at all** — the only implementors are two test fakes and the demo's in-memory
store. Nothing reads the pkg cache for file *contents*; `watcher.go` reads it
only for names. Meanwhile `daemon.go` announces `config.ServingPort()` to the
tracker and the keep-alive announces the whole cache, so this daemon advertises
packages it cannot serve and any peer acting on that entry gets
connection-refused. A dial failure correctly does not blacklist, so the cost is
one wasted attempt per peer.

This makes §5.3 larger than `HANDOFF.md` implies. It is not only a wire
migration: it must **build the cache-backed seeder that has never existed**.
Recorded in §6 as a known defect rather than fixed here — the fix belongs with
§5.3.

## Out of scope

§5.3 and the cache-backed `PackageSource`; mounting the seed server; the §4.2
per-remote-IP cap; wiring a `Reload()` trigger; and everything in §7 that needs
the FreeBSD host.

## Verification when this is executed

The gate — `go build ./... && go vet ./... && go test ./...` plus `gofmt` —
must pass with no FreeBSD, no `pkg` and no second machine. Extend the existing
malformed-row and collision fixtures in `internal/daemon/repodb_test.go` to
assert the two skip causes are counted separately; fixtures only, no real
catalogue. Confirm in `internal/peer` that `ErrHashMismatch` still blacklists
and nothing else does. No new size-check test — the read cap lands with §5.3.
Finally, read UC-02 steps 9, 9c and 11c against the peer spec's size-bound
section together and confirm they state one rule, not two.

## Uncertainty raised, per ground rule 2

Everything above was put to the owner as a question before being written down;
none of it was an implementer's reasonable interpretation. Still unanswered and
still blocking §5.3: the §4.2 **per-remote-IP cap**. A global concurrency limit
lets one hostile IP hold every slot, because nothing reclaims them, and a
per-remote-IP cap is in no spec.
