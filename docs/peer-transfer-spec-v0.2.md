# Peer Transfer Spec — v0.2

*The daemon↔daemon wire for package bytes (UC-02 fetch loop, UC-06 serving
side). This is a third, separate wire: `tracker-protocol-spec-v0.2.md` governs
daemon↔tracker and `mirror-facade-spec-v0.1.md` governs pkg↔daemon. Neither
says anything about this surface.*

**Status: decided by the spec owner, drafted by an implementing agent.** The
three decisions recorded below — HTTP transport, a peer-private URL namespace,
and where the fuzzing obligation lands — were taken by the owner. Everything
else is derivation from them plus measured facts about a real FreeBSD
repository, cited inline.

## What this wire replaces

v0.1 had no peer transfer spec. `internal/peerwire` was written as an interim
length-prefixed binary framing, and said so in its own header:

> ADR-001 pins peer transport to HTTP-over-TCP; this binary format is interim
> until the v0.2 wire spec lands.

This is that spec, so the interim format retires. Adopting HTTP is not an
amendment to ADR-001 — it is the first time the code matches what ADR-001,
`uc-02.puml` and `use-case-descriptions.md` §UC-02 have all said all along.

### The defect that forced the issue

The interim format sent a whole package as **one** frame with a 4-byte length
and a hard receive-side cap:

```go
const MaxPayload = 64 << 20              // 64 MiB
if length > MaxPayload { return ErrBadFrame }
```

Measured against the FreeBSD-ports repository database on a live 15.1-RELEASE-p1
host (37,835 packages): **493 packages (1.30%) exceed 64 MiB and therefore
cannot transfer at all**, including llvm19 (271.3 MiB), llvm20 (253.2 MiB), ghc
(202.9 MiB), rust (181.6 MiB), openjdk21 (167.7 MiB), libreoffice (142.9 MiB),
chromium (136.6 MiB) and gcc13 (84.5 MiB). Twelve exceed 1 GiB; the largest,
texlive-docs, is 2.83 GiB. These are precisely the packages a P2P mirror exists
to help with.

Three further faults, all structural rather than incidental:

1. **The cap is receive-side only.** `Encode` has no matching check, so a
   seeder builds and transmits a frame the requester is guaranteed to discard
   after reading five bytes.
2. **Both ends hold the entire package in memory** — `make([]byte, length)` on
   receive, and a second full copy inside `Encode` on send. The reference host
   has 1.0 GiB of RAM.
3. **The 30-second connection deadline covers the whole transfer**, so a large
   package fails on a slow link regardless of the size cap.

Chunked framing would fix (1)–(3) at the cost of hand-rolling a state machine,
resumption and truncation detection that HTTP already provides. The owner chose
HTTP.

## What this wire is

A seeding daemon runs an ordinary HTTP server on the port it announces to the
tracker as `servingPort`. A requesting daemon issues one `GET` per package.
Plain HTTP over TCP; no TLS, no authentication (integrity is end-to-end via the
repository database hash — see §Verification).

pkg is not involved. This wire is invisible to it.

## Request surface

```
GET /pkg/<name-version>
```

Example: `GET /pkg/gopls-0.22.0_1`

The namespace is **deliberately different from the mirror facade's**
`…/All/<name-version>.pkg`. Reusing the facade's path rule would have made every
seeding daemon a syntactically valid pkg mirror, and this wire is not one: it
serves no metadata, no catalog, and no signed anything. Two path rules and two
parsers is the accepted cost of keeping the two surfaces impossible to confuse.

Consequences of the split, all intended:

- A `.pkg` suffix is **not** used here. The identifier is the bare name-version.
- There is no repo-path prefix to ignore. The path is exact; anything else is a
  `404`.
- `packagesite.pkg`, `meta.conf` and friends have no representation on this
  wire at all.

### Name-version validation

The same minimal rule the rest of the codebase applies: non-empty, at most 255
bytes, no control characters (`internal/peer.validName`). The stricter
structural rule — a final hyphen separating a non-empty name from a
digit-initial version — is applied by the *cache* and *facade* layers, not
here; a seeder that holds a file under some other name is free to serve it, and
the requester's hash check is what actually decides.

### Methods

`GET` only. `HEAD` may be answered if it falls out of the implementation for
free (it does, via `http.ServeContent`); nothing depends on it. Anything else
is `405`.

## Responses

| Condition | Code | Requester action |
|---|---|---|
| Package held; body is the file | `200` | stream, verify, use |
| Not held (e.g. `pkg clean` since the last announce) | `404` | try next peer |
| Path is not `/pkg/<something>`, or the name-version fails validation | `400` | try next peer |
| Method other than `GET` | `405` | try next peer |
| Serving-side concurrency limit reached | `503` | try next peer |
| Anything else, or the connection fails | — | try next peer |

`200` responses carry `Content-Type: application/octet-stream` and an accurate
`Content-Length`. Error responses carry a short `text/plain` body for a human
with `curl`; the requester ignores it.

There is no error *message* type on this wire — the status line is the error.
The interim format's `MsgError` string has no successor.

The requester treats every non-`200` identically: log, move to the next peer.
The distinctions above are for operators reading logs, exactly as in the mirror
facade spec.

### The 404 obligation carries over

UC-06 §5b requires that a serving daemon which discovers it does not hold a
requested package sends a **full re-announce** to the tracker: if one entry has
drifted, others may have too. That obligation is unchanged by the transport and
attaches to the `404` path here.

It attaches to `404` **only**. A `503` must not trigger it: `404` means we have
discovered we no longer hold something we advertised, whereas `503` means we do
hold it and are refusing to serve it right now. Re-announcing on `503` would
flood the tracker precisely when the daemon is already at its limit
(`docs/adr/adr-002-serving-side-concurrency.md`).

## The size bound — what replaces `MaxPayload`

**There is no global size cap on this wire.** A fixed constant was only ever
necessary because the receiver had no way to know how large the package *should*
be. It does now.

`packages.pkgsize` in pkg's repository database is the exact file size in bytes.
This was verified byte-for-byte against cached files on the reference host, and
it sits in the **same row** as `cksum`, the lowercase-hex SHA-256 of the same
file. Both are already modelled: `daemon.RepositoryDatabase.ExpectedFileSizeBytes`
and `daemon.PackageHashes.ExpectedHash`.

The bound therefore becomes **per-package and exact**:

- If `Content-Length` is present and does not equal the expected size, abandon
  the peer before reading a single byte of body.
- Read the body through `io.LimitReader(body, expectedSize+1)`. More than
  `expectedSize` bytes is a protocol violation; abandon the peer.

This is a strictly *stronger* anti-DoS bound than 64 MiB — a hostile peer cannot
overrun by one byte — while removing the ceiling entirely.

**Abandoning is not blacklisting.** A peer that breaches the size bound is
dropped and the requester moves to the next holder; only a hash mismatch marks
it locally (UC-02 §11c). The size is a bound, not a verdict — a body of the
wrong length fails the hash anyway if read to completion, so a separate size
verdict would be a second route to the same conclusion.

**Invariant this depends on:** hash and size come from the same repository
database row, so any package that reaches the fetch path has both. The facade
already returns `404` when no expected hash is found, before any peer is
dialled. An implementation that has one and not the other is a bug, not a case
to handle gracefully.

## Verification and buffering (requester side)

The requester streams to a temporary file and hashes incrementally. It never
holds the package in memory and never hands unverified bytes to pkg.

```
tmp  := os.CreateTemp(cfg.TempDir, "jmj-*.pkg")     // removed on every path
sha  := sha256.New()
n, _ := io.Copy(io.MultiWriter(tmp, sha), io.LimitReader(body, want.Size+1))

reject unless n == want.Size
reject unless hex(sha.Sum(nil)) == want.Hash
```

- **Reject** means: discard the temp file, mark the peer in the local blacklist
  when the failure was a hash mismatch (UC-02 §11c — local only, never reported
  to the tracker), and continue to the next peer.
- On success the temp file is rewound and copied to pkg as the mirror response,
  then removed. Nothing above the copy buffer (~32 KiB) is ever resident.
- The temp directory is configurable and defaults to `os.TempDir()`. It is the
  only path the daemon writes to, per the hard constraints in `AGENTS.md`.

This resolves `mirror-facade-spec-v0.1.md` open question 4 ("the current fetch
path buffers in memory, so the buffer directory is unused"). It is now used, and
it is the only thing that makes a 2.83 GiB package transferable on a 1 GiB host.

## Serving side obligations

- Serve **straight from the pkg cache, read-only**. The daemon has no store of
  its own; this is unchanged from v0.1.
- Serve from an open file handle, not a byte slice, so the sender is also
  constant-memory. `http.ServeContent` over an `*os.File` satisfies this and
  lets the runtime use `sendfile` where available.
- **Never hash on this side.** Unchanged from v0.1 and from the announce path:
  the requester verifies end-to-end, and hashing here would be wasted I/O.
- Range requests are answered if the implementation provides them for free.
  The requester never sends one in v0.2; resumption is not in scope
  ("no additional features, just implement the use cases").
- **Bound concurrency with two non-blocking semaphores — one global, one keyed
  by remote IP — and reply `503` the moment either is full.** No queueing, no
  waiting, no `Retry-After`: the requester has other holders to try and pkg's
  own mirror behind those, so an immediate refusal is a fast fall-through where
  a wait would be a stall. Remote identity is the host half of `r.RemoteAddr`
  via `net.SplitHostPort` and is **never** read from a header — a cap keyed on
  client-supplied input is a cap the attacker sets. Both limits default to `0`,
  meaning unlimited, and both are configurable; the hostile-peer expectation
  justifies the mechanism, but nobody has measured a number. Diagnostics must
  name which cap fired and for which IP, because an attack and a misconfigured
  ceiling look identical in a bare count and have opposite remedies. Rationale
  and rejected alternatives: `docs/adr/adr-002-serving-side-concurrency.md`.

## Timeouts

The interim format's single 30-second connection deadline is withdrawn: it
capped the *whole* transfer, so any package slower than 30 seconds failed
irrespective of size.

| Phase | Bound |
|---|---|
| TCP dial | 5s |
| Response headers | 10s |
| Body transfer | **none** |

A slow multi-gigabyte transfer over a domestic uplink is legitimate traffic, and
a wall-clock deadline cannot tell it apart from a stall. The transfer is bounded
instead by the exact-size limit above and by ordinary TCP failure detection.

Serving side: bound the request headers (10s) and leave the response write
unbounded, for the same reason.

### A slow peer is out of scope

A peer that accepts the connection, sends headers, and then trickles bytes
indefinitely is **not a problem this spec solves**. It is the same situation as
a slow mirror, which pkg has always lived with and which nothing in this project
is required to improve on. The `LimitReader` bounds how *much* such a peer can
send; how *long* it takes is not the daemon's concern.

Do not add a minimum-throughput rule, a stall detector, or a transfer deadline
to address this. There is no user complaining about it.

## Robustness

The v0.1 requirement stands — *malformed input from untrusted machines must
never crash the daemon* — and the fuzzing obligation transfers with it.

Per the owner's decision, the fuzz target is **the peer server's HTTP surface,
end to end**: arbitrary bytes delivered to the server as a request, asserting
only that it never panics and always terminates the connection. This is broader
than the interim format's target, which fuzzed a framing function in isolation:
it exercises request framing, the path rule and the name-version check in one
target, on the same code path a hostile peer reaches.

The existing `internal/peerwire/testdata` corpus describes a format that no
longer exists and is discarded.

Note that request *framing* is now the standard library's responsibility. The
project-owned surface under test is the handler.

## Migration

Deleted outright:

```
internal/peerwire/            wire.go, fuzz_test.go, testdata/
```

Changed shape:

| Symbol | v0.1 | v0.2 |
|---|---|---|
| `peer.PackageSource` | `Get(nv) ([]byte, bool)` | `Open(nv) (io.ReadSeekCloser, int64, bool)` |
| `peer.FetchFromPeer` | `(addr, nv, hash) ([]byte, error)` | `(ctx, addr, nv, want, tempDir) (*os.File, error)` |
| `peer.Server` | raw `net.Listener` accept loop | `http.Server` |

Returning an open temp file rather than a byte slice is the change that carries
the streaming guarantee up to the facade; a `[]byte` return would silently
reintroduce whole-package residency at the caller.

`peer.Server.Serve`'s accept loop currently `continue`s on **every** `Accept`
error, including permanent ones, and hot-spins on a closed listener. Moving to
`http.Server` removes the hand-written loop and the bug with it.

## Deliberately unspecified — do not invent, ask

| Item | Status |
|---|---|
| Concurrency limits on the serving side | ~~Open. No cap on simultaneous seeds is specified.~~ **Decided by `docs/adr/adr-002-serving-side-concurrency.md`:** a global cap *and* a per-remote-IP cap, both non-blocking, both defaulting to `0` = unlimited, `503` when either is full. Specified under *Serving side obligations* above. It is admission control, not the forbidden bandwidth management — it sets no rate, no throughput floor and no deadline, and an accepted transfer runs exactly as fast as it otherwise would. |
| Resumption / `Range` on the requesting side | Out of scope for v0.2. The server may support it; the client does not use it. |
| Whether `PackageHashes` and `RepositoryDatabase` merge | Open. They are two views of the same repository row and this spec relies on both being present together, which strengthens the case for merging. Not decided here. |
| TLS, authentication, peer identity | None in v0.2. Consequences are availability-only and self-correcting, as with the tracker. |
| `HEAD` on the mirror facade | ~~Unchanged and still open.~~ **Answered by measurement** (`docs/logs/claude-pkg-mirror-verification.md` §7.3): pkg 2.7.5 issues only plain `GET` against a mirror — zero `HEAD`, zero `Range`, across a catalogue refresh, a `pkg fetch` and a real `pkg install`. See `mirror-facade-spec-v0.1.md` open question 1 for the scope limit on that result. |

## Definition of done

A requester and a seeder that demonstrate, in tests runnable on any OS with no
FreeBSD and no second machine:

1. A round trip: seeder serves from a file, requester verifies and returns it.
2. A package larger than the retired 64 MiB cap transfers successfully, with
   neither process holding it in memory.
3. Wrong bytes fail verification, the temp file is removed, and the peer is
   blacklisted locally.
4. A body longer than the expected size is cut off and rejected.
5. A `Content-Length` disagreeing with the expected size is rejected without
   reading the body.
6. `404`, `400`, `405` and `503` each advance the requester to the next peer.
7. The fuzz target survives arbitrary request bytes without panicking.
