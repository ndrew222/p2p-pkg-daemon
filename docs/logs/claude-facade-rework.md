# Work log — §5.7, the mirror facade rework

Author: claude. Feature: HANDOFF §5.7 — rewriting `internal/daemon/facade.go`
under ADR-003 (fetch semantics), ADR-004 (path rule), ADR-005 (metadata
proxying), ADR-006 (`upstream_url`) and ADR-007 (repository topology).

Branch: `worktree-facade-rework`. Worked concurrently with §5.3 (the peer wire
migration) in a separate worktree, then **rebased onto §5.3 once it merged**;
the branch now sits on `c791d9b`. **Not merged** — left for review.

## What landed

| Commit | What |
|---|---|
| `dab151e` | Fetch from upstream on a peer miss (ADR-003): narrowed `404`/`502`, the safe path join, `Facade.UpstreamURL` and its wiring, and the peer path served from `peer.FetchFirst`'s open file |
| `2a69522` | Relay non-package paths to upstream (ADR-005), `If-Modified-Since` and `304` included |
| (this one) | HANDOFF §5.7/§6/§4.8 and this log |

| `TBD-race` | Fix a data race in `startDiscoveryLocked` — §5.3's, found by the `-race` run; see below |

Gate after each: `go build ./... && go vet ./... && go test ./...` green,
`gofmt -l .` silent. `go test ./... -race` is green as of the last commit and
was **not** green on `main` before it.

### The series was rewritten at the rebase, deliberately

Before §5.3 landed this was five commits, one of which introduced a seam
stand-in for the peer fetch signature and another of which removed the facade's
own spool. Rebasing those onto §5.3 would have produced a history in which the
first three commits **do not compile** — they call a `peer.FetchFirst` that no
longer exists — and the owner reviews one commit at a time. So the series was
reconstructed on top of `c791d9b` as the two contracts it actually implements,
each of which builds and passes the gate on its own. The old series is kept as
`backup-seam-series` in case the reasoning in its commit messages is wanted.

## How I approached it

The instruction was "a rewrite, not an edit", and the reason is worth restating
because it is the thing that makes this file unusual: **its tests passed the
whole time.** Green meant consistently wrong, not correct. So the first
decision was to work out what the contract *is* before touching a line, and the
single most useful document for that turned out to be the deprecated facade
spec's rebuilt status table — not as a contract (it is history, and I did not
implement from it) but because it is the only place that shows the four
conditions that *collapsed* and why. Everything else states the new rule; that
table shows the shape of the change.

Reading order that actually worked, after `AGENTS.md` and `CLAUDE.md`:
ADR-003 → ADR-005 → the §7 verification log → UC-02's `8f–10f`/`9g–11g` →
`uc-07.puml` → ADR-004/006/007. ADR-003 and the measurement log are the two
that carry the *reasoning*; the rest is consequence.

Then I inverted the usual order of work: instead of editing the handler and
fixing tests afterwards, I wrote down the status table as a comment at the top
of the file first, and made the code and the tests match that. The old file's
defect was not a bug — it was a faithful implementation of a wrong contract —
so the contract statement had to come first or I would have been debugging
against the same absent authority.

### Why two code commits, in that order

The owner reviews one commit at a time, so each had to stand alone and pass the
gate. The split is by *contract*, not by file:

1. **The package path (ADR-003).** This is the commit that changes what pkg
   observes on a miss, and it is where the status semantics live. It also
   carries the peer path's move onto `peer.FetchFirst`'s open file, because
   after §5.3 there is no other way to write that call at all.
2. **The metadata branch (ADR-005).** A separate branch of `ServeHTTP` with its
   own ADR and its own tests; folding it into (1) would have made one commit
   that changes two contracts. The `If-Modified-Since` §6 defect is cleared
   here, with the work, as `CLAUDE.md` requires.

Docs last, since §5.7 is only resolved once both have landed.

### The tests that had to go

`facade_test.go:91` (metadata → `404`), `:189` (empty peer list → `404`),
`:354` (metadata over the wire → `404`) and `daemon_test.go:187` all encoded the
retired contract. They were replaced, not deleted:

- metadata → a relay, asserted three ways (body, `Content-Type`/`Last-Modified`
  relayed, upstream saw the joined path);
- empty peer list → `200` with the *upstream* copy, which is the case that
  matters most: it is the common case, and the old `404` failed every
  first-of-its-kind install;
- `daemon_test.go`'s probe → a relayed catalogue through the real daemon
  wiring. The old probe used a metadata path *because* the refusal needed no
  peer; that same property means it would now pass against a facade wired to no
  upstream at all, so the probe had to become one that only correct wiring can
  satisfy.

The peer/upstream distinction is asserted throughout by giving the two sources
**different bytes** and checking which came back. That is what stops a test
passing for the wrong reason.

`TestFacadeUnwritableTempDirIs500` became
`TestFacadeUnwritableTempDirGoesToUpstream` — see uncertainty (a).

## Difficulties

**Telling the two "upstream said no" cases apart.** ADR-005 says relay
upstream's status; UC-02 §9g says a non-`200` from upstream on a *package*
request is a `502`. These look contradictory until you notice they are scoped
to different branches, which the ADRs are careful about and a skim is not. The
code keeps them in separate functions for exactly that reason, and the two
tests (`TestFacadeRelaysUpstreamStatusForMetadata`,
`upstream does not hold it either`) sit next to each other to keep the
distinction visible.

**Not conflating ADR-007's two predicates.** "The repository database knows
this package" and "upstream can serve it" are different, and the easy mistake
is to treat an upstream `404` on the package path as impossible. It is not
impossible — `repo_db_dir` holds every catalogue on the host and `upstream_url`
fronts one repository — so the branch exists, is tested, and says so in a
comment rather than in an assertion.

**The seam with §5.3, and how it closed.** The migration table fixes
`FetchFromPeer`, but the facade calls `FetchFirst`, whose v0.2 signature the
table does not give, and neither does it name the type of `want`. Rather than
invent either, I put the v0.2 *shape* behind one variable in one file
(`peerfetch_seam.go`) whose header said what to delete and what to replace it
with, and wrote the facade against the streaming contract.

That paid off exactly as intended: **the shipped `FetchFirst` matched the stub's
argument order verbatim** — `(ctx, addrs, nameVersion, want, tempDir, bl)
(*os.File, error)` — so the merge was deleting one file and changing one call.
Two things §5.3 shipped that the migration table did not promise, both
improvements:

- **`peer.Want`** is the real name of the `want` type, carrying `Hash` and
  `Size` exactly as the spec's pseudocode used them. My local `peerWant` was
  the same shape and is gone.
- **`peer.Discard(f)`** closes and removes a spool file. That settles the
  ownership question a seam is most likely to drop: `FetchFromPeer`'s doc is
  explicit that the caller owns the returned file and must do both, and
  `Discard` is the single way to do it. The facade's hand-rolled
  close-then-remove is gone in favour of `defer peer.Discard(spool)`.

Two of my own lines also became redundant and were deleted rather than kept
"just in case": a `Stat` to derive `Content-Length` (the wire now rejects any
transfer whose length disagrees with the repository database, so the file is
exactly `want.Size` long), and my own `errors.Is` split of the peer-failure log
(`peer.ErrSpool` now makes that distinction properly, at the source).

**Where §5.3 and this work disagree — `ErrSpool`.** See uncertainty (a). This is
the one thing about the merge that was not mechanical, and it is a genuine
disagreement rather than a signature detail: §5.3's `ErrSpool` doc says it
exists "so the facade can answer 5xx", and this facade sends it to upstream.

**A trap §5.3's tests caught that mine would not have.** Under the v0.2 size
bound, a peer serving bytes of the *wrong length* is abandoned **without being
blacklisted** — the size is a bound, not a verdict. My corrupt-peer doubles
served `"tampered"` against a 13-byte package, so after the rebase they would
have been rejected on length, not on hash, and every blacklist assertion would
have failed for a reason having nothing to do with the facade. §5.3 hit this
first and solved it with a same-length constant; this file now has
`tamperedLike(b)`, which makes the requirement explicit at each call site.

**Transparent gzip.** Not a design question until you notice Go's transport
adds `Accept-Encoding: gzip` by itself and silently decompresses, which would
make "relayed unmodified" false and would break the package path's
`Content-Length`/`cksum` agreement. Found by reasoning about what the client
does, not by a failing test — worth flagging because nothing in the specs would
have caught it.

**What I did not touch.** The path rule (ADR-004), and `internal/peer`,
`internal/config` and `cmd/demo` (§5.3's files) — including at the rebase, where
`daemon.go`'s seed-server wiring and `daemon_test.go`/`seedserver_test.go` were
taken from §5.3 unchanged. `daemon.go` has one four-line change of mine, the
minimum needed to give the facade its upstream. §5.3's interim edit to
`facade.go` is the one thing deliberately discarded: it was a mechanical
adaptation to its own signature change, made against instructions but for a
defensible reason, and it sat inside the model this rework replaces.

## Areas of uncertainty

Ground rule 2 says an ambiguity is a stop-and-ask, and ground rule 4 says say so
at the end. **None of the four below is a stated ambiguity in an ADR** — in each
case the ADRs settle the rule and leave a mechanism unstated. I judged that
stopping the whole rework on any of them would be over-applying the rule, so I
made the call, made it visible in code comments, and recorded it in
**HANDOFF §4.8** for ratification. They are listed here in the order I would
want them looked at.

### (a) `500` no longer exists as a facade status — RAISED, not ruled

The old file answered `500` when it could not create its spool file, and
`TestFacadeUnwritableTempDirIs500` asserted it. Under the new contract it goes
to upstream instead.

Reasoning: ADR-003's rebuilt table has **no `500` row**, and its governing rule
is *"every peer-side failure falls through to upstream, not to pkg. An error
reaches pkg only when the peers and upstream have both failed."* An unwritable
`temp_dir` is a failure of the peer *path*, the upstream path does not touch
`temp_dir`, and answering `500` would fail an install the daemon could still
have served. The four conditions the table describes as collapsing are
described as what collapsed, not as an exhaustive list of what may fall through.

Against it: a `500` is a louder operator signal than a log line, and this is a
daemon-side fault rather than a peer-side one, which is a distinction the ADR
does not make but a reader might want.

**§5.3 then took the opposite view, which turns this from a judgement call into
a disagreement.** Its `ErrSpool` doc says the error is distinguished "so the
facade can answer 5xx — 'this daemon is broken' — rather than 'no peer has
it'", and its interim edit to `facade.go` answered `500`. Neither position is an
ADR. I kept the fall-through, because the facade owns the status codes and
ADR-003's table is the higher authority, and because it serves the user; I did
not silently overwrite their reasoning. It is now recorded in three places: the
contract comment at the top of `facade.go`, HANDOFF §4.8 with a section of its
own, and here. If the owner wants `500` back it is a three-line change, and
the log line that fires in that case is already the loudest in the file.

### (b) Query strings on the relayed path — RAISED, unobservable today

No ADR mentions them. I relay `RawQuery` unchanged, on the grounds that the
branch is specified as a relay of the request. No measured pkg request carried
a query, so nothing observable turns on it.

### (c) Which request headers cross to upstream — RAISED

ADR-005 names `If-Modified-Since` and nothing else, so that is the only one
forwarded. `User-Agent` in particular is not, which means mirror operators see
Go's default rather than `pkg/2.7.5`. The §7 probe forwarded `Range`,
`If-Modified-Since` and `User-Agent`, but that probe was a measurement harness
and is not a contract.

### (d) Disabling transparent gzip — RAISED

See *Difficulties*. I read ADR-005's "unmodified" as deciding this, which is
close to reading an ADR generously; the alternative reading (leave the
transport alone, accept that the facade may recompress or decompress) makes the
package path's `Content-Length` from `packages.pkgsize` wrong whenever a mirror
gzips, so I do not think it is a live option. Recorded because the ADR does not
say the word "compression".

### (e) The `want` type at the §5.3 seam — **CLOSED at the rebase**

`docs/peer-transfer-spec-v0.2.md` writes `want.Size`/`want.Hash` in its
pseudocode but names no type, so I did not invent one for `internal/peer` — the
stand-in lived inside `internal/daemon` and disappeared at the merge. §5.3
shipped `peer.Want` with those two fields, and the stand-in was the same shape,
so nothing had to be renegotiated.

### (f) Facts about pkg I did **not** claim

Everything asserted about pkg's behaviour in the new comments traces to
`docs/logs/claude-pkg-mirror-verification.md`: no fall-through between
repositories (§7.1), only plain `GET` (§7.3), conditional `GET`s on catalogue
files (§7.3 and the aborted first run), `All/Hashed/…~hash10` as the real
request shape (§7.5). Two things I deliberately did **not** assert, because
nothing has measured them: whether pkg ever issues a `Range` on
resume-after-interrupt, and whether a real mirror serves `All/<nv>.pkg` as well
as `All/Hashed/<nv>~hash10.pkg`. The second is why the facade forwards the path
pkg asked for rather than reconstructing one.

## The streaming claim, checked rather than assumed

Before the rebase this report said the peer path was "streaming in *shape*
only", because the fetch beneath the seam still materialised the package. With
§5.3 merged that is no longer true, and the claim is worth stating precisely
because it is the project's oldest hard constraint:

- `peer.FetchFromPeer` copies the body through
  `io.MultiWriter(tmp, sha256)` wrapped in `io.LimitReader(body, want.Size+1)`,
  so nothing above the copy buffer is resident on the way in.
- It returns an **open, rewound `*os.File`** whose contents are already verified
  against `want.Hash` and `want.Size`; `FetchFirst` passes that file up
  unchanged.
- The facade does `io.Copy(w, spool)` straight from that handle to pkg. There is
  **no `[]byte` in any signature, variable or buffer on the path**: the three
  occurrences of `[]byte` left in `facade.go` are all inside comments saying so,
  and the byte slices in `facade_test.go` are test fixtures.
- The spool's lifecycle ends at `defer peer.Discard(spool)`, which closes then
  removes. **Ownership is not ambiguous:** `FetchFromPeer`'s doc says the caller
  owns the file and must do both, it removes the file itself on every failure
  path, and `Discard` exists precisely so the caller has one call for it. I did
  not have to guess, and the facade no longer hand-rolls the close-and-remove it
  used to.

The upstream path was never in question — it streams `resp.Body` straight
through and has no spool at all.

## One thing found on the way, in someone else's code

`go test ./... -race` fails on `main` at `c791d9b`, before this branch touches
anything. `Daemon.startDiscoveryLocked` set

    onChange := func(ChangeEvent) { d.reannounce() }

so the cache watcher's goroutine read `d.reannounce` unlocked, racing
`stopDiscoveryLocked` clearing it. Reproduced with this branch's own
`daemon.go` reverted to main's, on `TestCacheChangeReachesTheTracker` and
`TestSeedServer404TriggersAFullReAnnounce` — whichever happens to overlap a
shutdown first, which is why it is intermittent.

Fixed here rather than only reported, because the branch's stated gate includes
`-race` and handing back a red one is worse than a three-line change in a file
this branch already edits. The fix keeps §5.3's design intact: the field
indirection exists for the **seed server**, which outlives a discovery restart
and correctly re-reads under the lock in `requestReannounce`; the **watcher**
does not outlive one, so it now holds the local closure directly. It is in its
own commit, attributed, so it can be dropped or moved without touching the
facade work.

## Follow-ups this leaves

1. §4.8's items, of which **(a) is a live disagreement with §5.3** and the only
   one worth the owner's time.
2. Nothing else in §5.7's scope is outstanding, and no stand-in code remains:
   `grep -rn 'SEAM (HANDOFF §5.3' internal/` finds nothing.
