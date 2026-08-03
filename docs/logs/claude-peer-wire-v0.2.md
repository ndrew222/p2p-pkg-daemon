# Work log — peer transfer wire v0.2

Author: Claude (implementing agent). Spec decisions by the owner.
Scope of this log: **documentation only.** No code changed. The migration
described by the spec is not implemented yet.

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

### 4. Timeouts

The spec removes the total-transfer deadline entirely and bounds only dial and
response headers. I am confident that capping a multi-gigabyte transfer by wall
clock is wrong, and equally that a minimum-throughput rule would be the
bandwidth management `AGENTS.md` forbids. What is left unaddressed is a peer
that accepts the connection, sends headers, and then trickles bytes forever. The
`LimitReader` bounds how *much* it can send but not how *long* it can take.
Flagged here rather than solved, because every solution I can construct is a
throughput rule wearing a different hat.

## What is deliberately not done

No code was changed. `internal/peerwire` still exists, `peer.FetchFromPeer`
still returns `[]byte`, and `peer.Server` still runs its hand-written accept
loop — which, separately, `continue`s on every `Accept` error including
permanent ones and hot-spins on a closed listener. Moving to `http.Server`
deletes that bug along with the loop, but that is the implementation commit,
not this one.

The migration table and definition of done in `peer-transfer-spec-v0.2.md` are
the checklist for that work.
