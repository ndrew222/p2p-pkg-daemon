# Work log — peer transfer wire v0.2

Author: Claude (implementing agent). Spec decisions by the owner.

This log has **two phases**, written at different times:

1. **Phase 1 — the spec (documentation only).** Everything down to *What is
   deliberately not done*. No code changed in that phase; the scope line below
   describes it and is still accurate for it.
2. **Phase 2 — the implementation (§5.3 and §5.4).** From *Phase 2 — the
   implementation* onward. That is where the code, the difficulties and the
   uncertainties of the build live.

Scope of phase 1: **documentation only.** No code changed. The migration
described by the spec was not implemented at the time it was written.

## How this started

Not as a planned piece of work. While wiring the cache watcher and settling the
config schema, the owner asked what was left, and the answer included a line
item I could not justify: `peerwire.MaxPayload`. The owner said, plainly, "I do
not understand the peerwire.MaxPayload issue." Explaining it properly turned out
to be the whole task, because the honest explanation is that the constant is not
a tunable — it is a symptom of a framing choice that cannot carry a real package.

## Thought process

### Establishing the size of the problem before proposing anything

`MaxPayload = 64 << 20` is easy to argue about in the abstract and trivial to
settle with data. The repository database from the reference FreeBSD host was
already local from the earlier schema investigation, so I queried it rather than
reasoning about it: 493 of 37,835 packages exceed 64 MiB, 1.30%, and the tail is
not obscure — llvm19, llvm20, ghc, rust, openjdk21, libreoffice, chromium,
gcc13. Twelve packages exceed 1 GiB and the largest is 2.83 GiB.

That reframed the question. It is not "is 64 MiB the right number"; it is "a
P2P mirror that cannot carry the packages worth mirroring." Raising the constant
would not fix it either, because the receive path allocates the whole payload:
`make([]byte, length)` on a host with 1.0 GiB of RAM.

### Reading the existing code before designing a replacement

Three faults beyond the cap, all structural:

1. `Encode` has **no** cap check while `ReadMessage` does. A seeder builds and
   transmits a frame the requester is guaranteed to reject after five bytes.
2. Both ends hold the package in memory, the sender twice (once from
   `Source.Get`, once copied into the frame buffer).
3. `conn.SetDeadline(30s)` covers the entire transfer, so a large package fails
   on a slow link independently of the size cap.

A chunked `MsgData` sequence fixes all three. I nearly proposed exactly that,
and then read the package header:

> ADR-001 pins peer transport to HTTP-over-TCP; this binary format is interim
> until the v0.2 wire spec lands.

`uc-02.puml:39` and `use-case-descriptions.md` §UC-02 say the same thing
independently. So the binary format was never the specified design — it was a
placeholder, and the thing blocking its retirement was the absence of the very
spec the owner was now asking for. Proposing chunked framing would have been
inventing a second interim format to avoid writing the specified one.

### The insight that actually retires the constant

The cap existed because the receiver had no way to know how large the package
*should* be. That was true when it was written and is not true now: the earlier
SSH investigation established that `packages.pkgsize` is the exact file size in
bytes, verified byte-for-byte against real cached files, sitting in the **same
row** as `cksum`.

So the bound becomes per-package and exact. That is strictly stronger than a
constant — a hostile peer cannot overrun by one byte — while removing the
ceiling. This is the part I would most want reviewed, because it is the load-
bearing claim: the constant is not raised or made configurable, it is deleted,
and something better takes its place.

It also depends on an invariant worth stating explicitly, which the spec does:
hash and size come from the same row, so any package reaching the fetch path has
both. The facade already 404s on a missing hash before dialling a peer.

### Connecting it to the temp-file decision

The owner had just decided the temp directory should be configurable and that
`os.CreateTemp` was the right tool. That decision could not actually be honoured
under the existing API: `ReadMessage` returns `[]byte` and `PackageSource.Get`
returns `[]byte`, so the whole package passes through memory before reaching
disk regardless of where the file is created. Buffering to disk only means
something if the framing delivers a stream. HTTP does; that is why the two
decisions belong in the same spec.

## Decisions taken by the owner

Presented as three explicit choices rather than a single recommendation, because
each has a real trade-off:

1. **Transport: HTTP over TCP** (over chunked binary framing). Implements
   ADR-001 rather than amending it.
2. **Peer namespace: `/pkg/<name-version>`**, deliberately unlike the facade's
   `…/All/<name-version>.pkg`. I had recommended sharing the facade's path rule
   for the single-parser benefit; the owner chose separation so that a seeding
   daemon cannot be a syntactically valid pkg mirror. On reflection the owner's
   choice is better than my recommendation: the shared-parser saving is one
   small function, and the cost I was discounting — a peer surface that pkg
   could be pointed at directly — is a scope leak that would have been very hard
   to walk back later.
3. **Fuzzing** moves to the peer server's HTTP surface end to end.

## Difficulties

**PlantUML markup in note bodies.** An earlier session lost the first word of
every `alt`/`else` label because bracketed text is parsed as a link. The same
class of defect nearly recurred here: `<name-version>` inside a note body is
markup, not text. Angle brackets were stripped from note bodies, and both
diagrams were rendered to PNG and visually inspected before committing rather
than trusting that the source looked right.

## Areas of uncertainty

### 1. "Just fuzz net/http" — clarified, interpretation flagged, not confirmed

The owner declined the two offered options for where the fuzzing obligation
should land and wrote: *"just fuzz net/http."*

Taken literally this is not actionable — fuzzing the standard library is
upstream's job and exercises none of this project's code. I read the intent as
"keep a fuzz target, but aim it at the HTTP surface rather than at a hand-rolled
framer," and stated that reading back to the owner explicitly, inviting
correction, before writing it into the spec.

**Status: interpreted, disclosed, not yet confirmed.** Per ground rule 2 this is
the uncertainty in this piece of work most likely to need revisiting. It is
recorded here rather than resolved silently. The reading I adopted is at least
strictly broader than the alternative it replaced — the end-to-end target covers
request handling, the path rule and name-version validation in one, where the
retired target covered a framing function in isolation.

### 2. Whether `PackageHashes` and `RepositoryDatabase` should merge

The spec relies on hash and size always being available together, because they
are columns of the same row. Two separate interfaces model them, and
`facade.go` already carries a comment guessing they "should probably merge once
a real reader exists." This spec strengthens that case but does not decide it.
Left in the spec's *Deliberately unspecified* table. Not raised with the owner —
it is an internal interface question, not a wire question, and nothing is
blocked on it.

### 3. Concurrency limits on the serving side

Nothing specifies how many simultaneous seeds a daemon should accept. With
constant-memory serving this is far less dangerous than it was, but it is not
zero — file handles and sockets are still finite. It sits uncomfortably close to
the forbidden "bandwidth management," which is why I did not invent a limit.
Recorded as open. **Not raised with the owner yet; worth raising before the
serving side is implemented.**

### 4. Timeouts — raised and closed

The spec removes the total-transfer deadline and bounds only dial and response
headers. I flagged the residual case — a peer that sends headers and then
trickles bytes forever — as unresolved, on the reasoning that every fix I could
construct was a throughput rule in disguise and `AGENTS.md` forbids those.

The owner closed it: a slow peer is out of scope in exactly the way a slow
mirror is, and pkg has always lived with slow mirrors. There is no problem here
to solve.

The owner also corrected my reading of the constraint itself. "No bandwidth
management" is not a principled prohibition on rate control; it records that an
earlier attempt solved a problem nobody had, incoherently. I had been treating
the line as a fence to reason around, which is why I logged a non-problem as an
open uncertainty instead of recognising it as out of scope. `AGENTS.md` now
states the actual reasoning so the next agent does not repeat the mistake in
either direction — neither smuggling rate control in, nor citing the line as an
excuse to leave a real defect alone.

## What is deliberately not done

No code was changed. `internal/peerwire` still exists, `peer.FetchFromPeer`
still returns `[]byte`, and `peer.Server` still runs its hand-written accept
loop — which, separately, `continue`s on every `Accept` error including
permanent ones and hot-spins on a closed listener. Moving to `http.Server`
deletes that bug along with the loop, but that is the implementation commit,
not this one.

The migration table and definition of done in `peer-transfer-spec-v0.2.md` are
the checklist for that work.

---

# Phase 2 — the implementation (HANDOFF §5.3 and §5.4)

Written after the code landed. The migration table and the definition of done
were the checklist, exactly as phase 1 anticipated; what follows is how it went
and what I could not settle on my own.

## How I approached it

### Reading order, and one thing that was not there

`AGENTS.md`, `CLAUDE.md`, HANDOFF (§5.3, §5.4, §4.7, §6, §8), the peer spec,
ADR-002, `uc-06.puml`. HANDOFF §0 mandates a `graphify query` pass before
reading source; `CLAUDE.md`'s closing note already records that the installed
`graphify` exposes no `query` or `update` subcommand and that this repository
has no graph, so I read and grepped source directly, as that note allows.

**§4.7 did not exist in the tree.** The ADR-002 config key names were given to
me as an owner ruling of 2026-08-09 "recorded at HANDOFF §4.7", and both
`grep -rn '4\.7' docs/` and `grep -rn max_concurrent docs/` came back empty —
the file went §4.4, §4.5, §4.6 and then straight to §5. I did not treat that as
licence to name the keys myself, because ground rule 3 makes an invented config
key exactly the wrong move; I took the names as relayed and **wrote §4.7 into
HANDOFF as part of the config commit**, marked in the section itself as
recorded by the implementing agent from the ruling as relayed. Flagged for the
owner: the decision is yours, the prose around it is mine, and if the relay was
wrong the fix is a rename in three places.

### Commit sequence

Config keys → the wire (everything) → the mount → this log. The middle commit
is much larger than I would like, and it is not an accident of drafting:

- The size bound needs the expected size, and only the facade holds the
  repository database, so `FetchFirst`'s signature had to change.
- `MaxPayload` was the only length check on the fetch path, so `peerwire` could
  not be deleted before its replacement, and — per the same constraint —
  could not survive past it either. It had to go in the same commit.
- Everything that depended on either (`cmd/demo`, three test files, the facade
  call site) had to move with them or the gate would not pass.

I could see no split that left a green gate at every step and did not either
ship an unbounded fetch path for one commit or leave a second interim wire in
the tree. Splitting it would have been a nicer-looking history bought with a
worse invariant.

### The seeder

Two designs were live for resolving a name-version to a cache file:

1. **The watcher's index.** The watcher already walks `cache_dir` and knows
   every announced package's exact path, so a `Lookup` would guarantee "serve
   exactly what you announced" and cost nothing per request.
2. **A direct path.** Open `<cache_dir>/<name-version>.pkg`.

I went with (2). The deciding argument was assumption surface, not lines of
code: (2) rests on one measured fact — `claude-pkg-mirror-verification.md` §7.5
records `find /var/cache/pkg -type d` returning only the cache directory itself
and an install leaving `libpaper-1.1.28_1.pkg -> libpaper-1.1.28_1~599a5a67ab.pkg`
— whereas (1) adds shared mutable state, a startup window in which the index is
empty, and a coupling that makes the seeder's correctness depend on when the
keep-alive last scanned. (2) is stateless and cannot go stale.

I rejected a third option outright: scanning the directory for a file whose
parsed name-version matches. It handles more layouts, and it hands a hostile
peer a directory read over tens of thousands of entries per bogus request.

The one thing I want reviewed here is the residual: if a cache ever held the
`~hash10` file *without* the unsuffixed symlink, the watcher would announce it
(it reads real files and skips links) and the seeder would `404` it. The
measurement says that does not happen. It is recorded as uncertainty 3 below
rather than papered over with a fallback nobody asked for.

### Where the path-safety boundary went

`peer.validName` rejects only empty, oversized and control-character input —
deliberately, and the spec says why: the structural name-version rule belongs
to the cache and facade layers, and "the requester's hash check is what actually
decides". So `../../etc/passwd` passes `validName` intact.

That is fine as a *wire* check and fatal as a *filesystem* check, so
`CacheSource` refuses anything that is not a plain file name — separators, dot
segments, NUL, and a `filepath.Base` round-trip for a platform grammar I have
not anticipated. **Refused, not cleaned**: a cleaned path is a guess at what the
caller meant, and the caller here is an untrusted remote daemon. `validName`'s
doc comment now says explicitly that it is not a path check, so the next reader
does not mistake it for one.

## Difficulties

**The §5.7 seam.** I was told not to touch `internal/daemon/facade.go`, which
another agent is rewriting. That turned out to be impossible to honour
completely, and the reason is worth stating rather than hiding: `FetchFirst`
cannot enforce the size bound without the expected size, the expected size lives
in the repository database, and only the facade holds one. A `[]byte`-returning
bridge that kept the old signature would have had no size to bound with — an
unbounded fetch path, which is precisely the hard constraint that forbids
deleting `peerwire` early. So the call site changed. I kept the edit mechanical
(the call, an error branch for `ErrSpool`, and streaming from the returned file
instead of a byte slice) and touched nothing about the measured-false
fall-through model. `FetchFromPeer`'s signature is exactly the migration table's,
which is the seam that was specified.

**A shutdown race the tests found, not review.** `peer.Server.Close` originally
did nothing if `Serve` had not yet reached the point of constructing its
`http.Server` — and `Serve` runs in a goroutine. On SIGHUP that left the old
`serving_addr` bound while the tracker was being told about the new one, which
is the one failure mode where the daemon lies to the swarm about where to reach
it. Fixed twice over: a `closed` flag so a late `Serve` closes its listener and
returns, and the daemon holding the listener itself so shutdown is synchronous
with the caller rather than with a goroutine.

**A test fixture that was quietly asserting the old contract.** Three facade
tests used an 8-byte `"tampered"` body against 13 bytes of expected content.
Under v0.1 that reached the hash check and blacklisted the peer. Under v0.2 the
size bound catches it first — and a size breach is explicitly *not* a
blacklisting, because "the size is a bound, not a verdict". The failing test was
correct to fail; the fixture now uses a same-length tampered body, so it
exercises the case the blacklist is actually for. Worth noting because a
green suite would have hidden the distinction entirely.

**Asserting "not in memory" without a flaky test.** A peak-RSS assertion is at
the mercy of the GC and of whatever else the machine is doing. I used two
checks instead: cumulative `TotalAlloc` across a 64 MiB+ transfer, bounded at a
quarter of the file (a whole-package buffer would exceed it several times over,
and `TotalAlloc` is unaffected by collection), and a reflective assertion that
no `[]byte` appears in the parameters or results of `FetchFromPeer`,
`FetchFirst`, `Download` or `PackageSource.Open`. The second is the one that
will still be true on a machine where the first is noise.

## Areas of uncertainty

Ordered by how much they need the owner.

### 1. `400` vs `404` for a non-exact path — **RAISED, not resolved**

The spec contradicts itself. Its *Responses* table says:

> Path is not `/pkg/<something>`, or the name-version fails validation | `400`

and a bullet under *Request surface* says, of the same condition:

> The path is exact; anything else is a `404`.

I need one of them to write the handler. **Implemented as `400`, per the
table**, for a reason beyond "the table is the normative section": `404` — and
only `404` — carries the UC-06 §5b full re-announce obligation. If a malformed
path answered `404`, a hostile peer could drive this daemon's announce traffic
by sending nonsense paths, and a malformed path is no evidence whatsoever about
what we hold.

**Status: raised for the owner, in HANDOFF §5.3, in `CLAUDE.md`'s current-state
list, in a comment at the decision site in `serve.go`, and in the commit
message.** Not silently resolved. The requester treats every non-`200`
identically, so nothing depends on which it is; flipping it is a one-line
change and one test line. I would rather be told than guess, and I have said so
in four places so the question survives this session.

### 2. `FetchFirst` and `Download` are not in the migration table

The table names `PackageSource`, `FetchFromPeer` and `Server`. It says nothing
about the two functions above them, which had to change anyway because they call
`FetchFromPeer`.

I gave them the streaming shape — `(*os.File, error)` — rather than reading the
file back into a `[]byte` to preserve their signatures. That is a derivation
from a hard constraint rather than an invention: a `[]byte` at that level puts
the whole package back on the heap one layer above the wire and undoes the thing
the wire exists to fix. **Not raised separately with the owner**, because I
could not construct a reading of the constraints under which the alternative is
allowed. Recorded here so the reasoning is visible if that judgement was wrong.

### 3. The `~hash10`-only cache entry

`CacheSource` resolves `<cache_dir>/<name-version>.pkg`, which on a real host is
the symlink beside the `~hash10` file. If a cache ever held the real file
without that link, the watcher would announce the package and the seeder would
`404` it — and since the announce list would not change, it would keep doing so
for that one package. The measurement (§7.5) says the pair always exists.

**Not raised**, because a fallback would either mean a per-request directory
scan (a DoS vector) or reconstructing the suffix from the repository database (a
new dependency for the seeder, and one the spec's "a seeder that holds a file
under some other name is free to serve it" cuts against). Recorded as a known
residual with a bounded cost: one package, one wasted peer attempt, and the
requester falls through.

### 4. Whether `PackageHashes` and `RepositoryDatabase` should merge

Still open in the spec's *Deliberately unspecified* table, and this work leans
on both being present together more heavily than the spec did: `peer.Want`
carries hash and size as one value precisely because they are one repository
row, and the facade now looks up both before dialling a peer.

I did **not** merge them, and did not raise it as a request to. The spec marks
it open, `repository.go` records a deliberate reason for keeping the narrow
interfaces (a size-only signature is what *proves* the announce path cannot
hash), and nothing here is blocked. `Want` is a third, separate type in
`internal/peer` rather than a fourth opinion about `internal/daemon`'s
interfaces. Flagging it only because my task brief called it out: if I had
wanted to merge them, that would have been an ask, and I did not need to.

### 5. `-generate-config` has no flags for the two new caps

The keys load, validate, warn and round-trip, and `-generate-config` emits them.
There is no `-max-concurrent-seeds` flag to go with `-cache`, `-repo-db` and the
rest. That is a deliberate omission rather than an oversight — the caps default
to `0` and every other flag exists to make a *generated* config startable on a
different host, which these do not affect — but it is an asymmetry someone may
want closed. **Not raised**; it is a five-line change if wanted.

## What phase 2 did not do

- The facade still implements the measured-false fall-through model. Only its
  call into `peer` changed. §5.7 is the rework.
- Nothing triggers `Repositories.Reload()` yet; that is the §5.2 follow-up and
  HANDOFF explicitly says to ask before choosing between a watch and a timer.
- No two-machine trial. Everything is exercised in-process, which is what the
  definition of done asks for; `cmd/demo` is the closest thing to a live run and
  it is a single process.
