# ADR-009: The facade's status set has no `500`, and a spool failure is a peer-path failure

**Status:** Approved by Andrew (ruled 2026-08-09; drafted from that ruling)

Resolves `docs/logs/HANDOFF.md` §4.8(a). Completes ADR-003's status table rather
than changing it. Evidence: `docs/logs/claude-facade-rework.md`.

## Context

§5.3 and §5.7 were worked concurrently and shipped opposite readings of the same
error.

- `internal/peer`'s `ErrSpool` — a local failure of `temp_dir`, not any peer's
  fault — carried a doc comment saying it is distinguished *"so the facade can
  answer 5xx — 'this daemon is broken' — rather than 'no peer has it'"*, and
  §5.3's interim edit to `facade.go` did answer `500`.
- The §5.7 rework sends `ErrSpool` to upstream like any other peer-path failure,
  on the grounds that ADR-003's rebuilt table has no `500` row and its governing
  rule reserves an error to pkg for the case where **both** sources have failed.

Neither position was an ADR. The facade owns its status codes, so its reading is
what shipped, and the disagreement was recorded at §4.8(a) rather than resolved
by whichever file happened to be written second — which is the case this ADR now
settles.

The concrete question: **an unwritable `temp_dir` — what does pkg see?**

The two answers differ in what they cost.

- **`500`.** The operator finds out at once, because every install fails until
  they fix it. It is also a lie about the request: the daemon *can* serve it, from
  upstream, without touching `temp_dir` at all.
- **Fall through to upstream.** Installs keep working. The daemon degrades into
  a plain proxy — the swarm stops helping it and it stops helping the swarm —
  and the only signal is a log line.

There is a third property worth naming: `pkg` cannot act on a `500` either way.
§7.1 measured that a facade error does not send pkg to another repository, so a
`5xx` here does not route around the broken daemon; it ends the install.

## Decision

**The facade's status set is exhaustive, and `500` is not in it.**

| Status | When | Source |
|---|---|---|
| `200` | peer bytes, hash-verified | UC-02 §10 |
| `200` | upstream bytes, streamed | UC-02 §8f–10f |
| `400` | under `All/` but the stem is not a name-version | ADR-004 |
| `404` | **only:** provably absent from the repository database | ADR-003 |
| `405` | anything but `GET` | ADR-004 |
| `502` | **only:** peers *and* upstream both failed | UC-02 §9g–10g |
| relayed | a non-package path answers with upstream's own status (`200` / `304` / `404` / …) | ADR-005 |

**`peer.ErrSpool` is a peer-path failure and goes to upstream.** An unwritable
`temp_dir` disables the peer path and nothing else; the upstream path does not
spool. ADR-003's governing rule — an error reaches pkg only when both sources are
gone — decides this on its own, and this ADR states it so that the next reader
does not have to derive it.

**`internal/peer` does not decide the facade's status codes.** `ErrSpool` exists
so the fetch loop can stop instead of blaming every holder in turn for this
daemon's fault; that is `internal/peer`'s own reason and it is unchanged. What it
must not do is assert what the caller answers. The caller decides.

**The cost is accepted, and the mitigation is the log.** A daemon with a broken
`temp_dir` keeps installing packages and quietly stops participating in the
swarm. The log line that fires on that path is the loudest in `facade.go` and
names the condition and the remedy explicitly. This ADR does not add an operator
alarm, a health endpoint or a startup probe for it — none of those is specified
anywhere, and inventing one here would be exactly the move ground rule 2 forbids.

## Consequences

**§4.8(a) closes and the two files stop disagreeing.** `internal/peer/fetch.go`'s
`ErrSpool` comment loses the clause about the facade answering `5xx` and points
here instead; `facade.go`'s header stops flagging a live disagreement and cites
this ADR.

**A regression test now pins it.** The behaviour was previously visible only as
prose in two files, one of which said the opposite. A test that asserts an
unwritable `temp_dir` produces `200` from upstream — and `502` only when upstream
also fails — is what stops a later reader "fixing" one comment to match the
other.

**`500` remains unavailable for anything else too.** This is a general statement
about the facade, not a special case for `ErrSpool`. If some future condition
genuinely needs to tell pkg "this daemon is broken", it needs an ADR that
reopens the table — not a spare `5xx` taken because one was free.

**Nothing about the peer wire changes.** `internal/peer`'s own status codes
(`200`/`400`/`404`/`405`/`503`) are `docs/peer-transfer-spec-v0.2.md`'s and are
untouched. The two surfaces are deliberately unlike each other and this ADR
governs only the facade.
