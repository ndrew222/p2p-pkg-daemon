# Work log — local peer blacklist + accept-loop fix (UC-02)

Author: Claude (agent). Branch: `claude/branch-handoff-j5u55i`.
Picks up the two items `docs/logs/claude-mirror-facade.md` left as open work
rather than as open questions.

## Why these two, and nothing else

The mirror-facade handoff ends with a list of loose ends. Most of them are
spec ambiguities — repo-DB location and hash format, the facade's listen port,
`HEAD`, `Range`, the peer transport contradiction, the temp buffer — and
AGENTS.md ground rule 2 says those stop and wait for the spec owner. They are
still waiting; I did not touch them and did not pick interpretations.

Exactly two items on that list are not ambiguities:

1. **The local blacklist.** UC-02 specifies it in two places — §7 "trying the
   peers in the order returned, skipping any on its local blacklist" and §11c
   "mark the peer untrusted in a local blacklist (local only; never reported to
   the tracker)". That is a specified requirement with no implementation, i.e.
   a gap, not a question. The handoff called it "a genuine gap against UC-02".
2. **`peer.Server.Serve` spinning on a closed listener.** A plain defect the
   previous session found and flagged rather than fixed, because it belonged to
   someone else's file mid-merge. There is nothing to interpret.

## The blacklist

`internal/peer/blacklist.go`. An address set behind a mutex, and deliberately
nothing more.

**Where it lives.** On the fetch side, not the mirror facade, because UC-02
puts it in the peer loop and because a facade-local list would be pointless —
the list only earns its keep by outliving a single request. Concretely it is a
`peer.Blacklist` value field on `Facade`, so one daemon has one list for its
whole run, and `internal/peer` owns the type.

**What blacklists a peer.** A hash mismatch, and only that. UC-02 gives
unreachable peers and timeouts their own error states (§8e/§9e) whose entire
instruction is "move on to the next peer" — no marking. Blacklisting on
connection failures would evict most of a healthy swarm after one bad network
minute, and §11c is specifically about a peer that *sent* corrupt bytes. Tested
both ways round.

**Restructuring that came with it.** The facade had inlined its own copy of the
peer loop (the handoff explains why: `peer.Download` collapses "tracker
returned nothing" and "every peer failed" into one `ErrNoPeers`, and the mirror
surface answers 404 vs 502 to those). Adding blacklist logic would have meant
two copies of it, so the loop is now `peer.FetchFirst(addrs, nameVersion,
expectedHash, *Blacklist)`, called by both `Download` and the facade. The
facade keeps the distinction it needs by checking `len(addrs)` itself before
calling, which it already did. `Download` gained a `*Blacklist` parameter; its
only caller was a test.

Status codes are unchanged. A list where every peer is already blacklisted
returns the same 502 as a list where every peer fails live — in both cases
peers claimed the package and no verified bytes came back, which is what that
code means. It is noted in the spec doc.

## The accept loop

`Serve` logged and `continue`d on every `Accept` error, permanent ones
included, so closing the listener span the loop at full speed. It now returns
on a permanent error and backs off 5ms→1s on a temporary one, the same shape
`net/http.Server.Serve` uses. Shutdown therefore works, and the facade tests no
longer have to leak their test listeners to avoid the spin — that workaround
and its comment are gone.

## Difficulties

Little, honestly, beyond deciding what *not* to do. The temptation with a
blacklist is to add a TTL, a failure counter, a persistence file — all of which
are decisions nobody has made. Writing the type took ten minutes; deciding it
should have no expiry took longer and is the part worth reviewing.

## Areas of uncertainty

| Uncertainty | Clarified? | Outcome |
|---|---|---|
| Do blacklist entries expire? | **No** — not asked; UC-02 is silent | Implemented with **no expiry**: entries last as long as the process. Inventing a TTL would be inventing a number. If the owner wants one, it is a field on `Blacklist` and one check in `Blocked`. Documented in the type comment. |
| Is the blacklist persisted across restarts? | **No** — not asked | **Not persisted.** UC-01 says the daemon holds write permission on its buffer directory and config path only, so persisting would need a storage location nobody has specified. The list starts empty every run. |
| Is a peer blacklisted per package, or for everything? | **No** — UC-02 says "mark the peer untrusted", unqualified | Implemented **per peer, across all packages**, the literal reading. A peer serving corrupt bytes is untrusted as a peer. |
| Should an exhausted-but-all-blacklisted list get its own status code? | **No** | Reused 502; rationale above. |
| Everything else the mirror-facade log lists as open | **No** — still open, still with the owner | Untouched. The facade is still not mounted (no port field), still wired with a `nil` repo DB, still 405s `HEAD`, still ignores `Range`. Nothing works end to end until a repo-DB reader lands. |

## Verified

`go build ./...`, `go vet ./...`, `go test ./... -race` all clean. New tests:
blacklist semantics and concurrency, corrupt-peer marking, unreachable-peer
non-marking, all-blacklisted, cross-request persistence through the facade, and
`Serve` returning on a closed listener.
