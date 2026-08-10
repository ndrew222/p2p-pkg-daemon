# Work log — the two-machine trial, 2026-08-10

The last genuinely unproven thing in this project. Everything before today was
one host talking to itself over loopback, which exercises every code path and
proves nothing about a peer that is not `127.0.0.1`.

**Every item HANDOFF §7.10 and the demo guide §3.4 listed as not covered is now
covered**, plus one that was not on the list (ADR-002's `503`). Nothing failed,
and nothing behaved differently from the design.

Addresses are placeholders — this repository is public:

| Name | What it was | Role |
|---|---|---|
| **A** | FreeBSD 15.1-RELEASE-p1, pkg 2.7.5, Vultr, public | tracker **and** daemon (tracker-only in phase 4) |
| **B** | FreeBSD 15.1-RELEASE-p1, pkg 2.7.5, Vultr, public | daemon; the hostile peer in phase 2 |
| **C** | Linux, behind NAT | fetch-only daemon, empty cache, catalogue copied from A |

Both FreeBSD boxes report `FreeBSD:15:amd64`, so a row in one catalogue is a row
in the other — which is what makes a hash from A's catalogue the right
expectation for bytes from B.

## Approach

The owner supplied the second box mid-session and offered to run `trac` at home.
**Home does not work without a forwarded port, and that was already measured**:
earlier the same day, A dialled the home address and got `i/o timeout` — inbound
is blocked by NAT. So the tracker went on A, which has a public address, and the
"tracker separate from both peers" case was covered instead by phase 4, where A
runs the tracker with no daemon of its own.

Each phase was built so its result could not be ambiguous. That mattered more
than it sounds: the first attempt at the hostile-peer test was set up with the
honest holder also in the tracker's reply, and the tracker returned the honest
one first, so the corrupt copy would never have been reached. Holder sets were
curated per phase — by pointing a daemon at a cache directory holding exactly
what that phase needed — until each test had exactly one possible path.

Both boxes were returned to their baselines and verified afterwards.

## Phase 1 — an honest swarm

Tracker on A; daemons on A and B, both seeding from the real `/var/cache/pkg`;
C fetching, holding nothing. Both boxes registered under their real public
addresses with no configuration, because the tracker keys on the connection's
source address.

### A 98 MB transfer, and constant memory on a real link

`gcc14-14.2.0_6`, held only by B, fetched by A through its own facade:

```
peer: fetched "gcc14-14.2.0_6" from <B>:9102 (98852086 bytes, verified)
facade: served "gcc14-14.2.0_6" from a peer (98852086 bytes)
peer: served "gcc14-14.2.0_6" to <A> (98852086 bytes, streamed from the cache)   [B]
```

| | |
|---|---|
| bytes | 98,852,086 |
| wall clock | **2.27s** — about 43.5 MB/s between two Vultr boxes |
| **requester RSS** | 27,992 KiB before → **28,012 KiB peak** (+20 KiB) |
| **seeder RSS** | 27,560 KiB before → **27,560 KiB peak** (no change) |
| `temp_dir` after | empty |
| SHA-256 | `3fda6f2c76…`, matching the `~hash10` on B's cached file |

**This is the first measurement of the constant-memory constraint on a real
transfer.** `AGENTS.md` forbids either end holding a package, and the earlier
evidence for it was a code review plus a 43-byte demo. A 98 MB package moved
between two machines and neither process grew by more than a rounding error.

### Both directions, twenty-one seconds apart

A fetched `gcc14` from B at 08:52:08; B fetched `fish-4.6.0_2` (4,842,922 bytes)
from A at 08:52:29. Each box served and was served in the same swarm — the
"nothing has both served and been served" gap, closed.

### Selection among several holders

With both boxes holding `curl-8.21.0`, C's fetch saw both:

```
discovery: query pkg="curl-8.21.0" -> 2 peers
peer: fetched "curl-8.21.0" from <B>:9102 (1906679 bytes, verified)
```

The requester took the first holder offered and stopped; A was never contacted.
Peer order comes from a Go map iteration in the tracker and is effectively
random per query — which is worth knowing, because it is *why* the hostile-peer
phase had to curate holder sets rather than rely on ordering.

## Phase 2 — a hostile peer, over a real link

B was given a copy of `git-2.54.0` with **the right size and the wrong bytes**
(one byte overwritten at offset 4,000,000; 9,368,020 bytes either way, SHA
`435021ee9e…` instead of `86f327f30f…`). Same size is the point: `SanityFilter`
compares sizes, so a same-size forgery is announced normally and **only the
requester's hash check stands between it and the caller**.

A was restarted on a curated cache holding neither `git` nor `gcc14`, making B
the sole holder of both.

### A hash mismatch blacklists, and the corrupt bytes never surface

```
discovery: query pkg="git-2.54.0" -> 1 peers
peer: blacklisted <B>:9102: corrupt bytes for "git-2.54.0"; 1 peer(s) blacklisted until restart
facade: "git-2.54.0": 1 peer(s) tried, none served a verified copy … going to upstream
facade: served "git-2.54.0" from upstream (9368020 bytes)
```

The caller received SHA `86f327f30f…` — **the correct package**. The forged
bytes were spooled, hashed, rejected and deleted, and the request was satisfied
from upstream instead. From pkg's point of view nothing happened at all.

### The blacklist is whole-peer, not per-package

The very next request was for `gcc14`, which B holds **honestly** and nobody
else holds:

```
discovery: query pkg="gcc14-14.2.0_6" -> 1 peers
peer: skipping blacklisted peer <B>:9102 for "gcc14-14.2.0_6"
facade: served "gcc14-14.2.0_6" from upstream (98852086 bytes)
```

No dial, no attempt — and a correct 98 MB package from upstream. This is the
documented design (`claude-peer-blacklist.md`: local, no expiry, whole-peer) and
its cost is now visible: one bad package costs a peer *all* of its usefulness to
that daemon until restart. That is a deliberate trade and it is the first time
anyone has watched it happen across machines.

## Phase 3 — recovery inside the swarm

The remaining fetch-loop case: a mismatch where **another holder exists**, so the
requester should retry rather than fall through. A was given the tampered copy
this time and B the honest one, so whichever the tracker offered first, one was
corrupt.

```
discovery: query pkg="git-2.54.0" -> 2 peers
peer: blacklisted <A>:9102: corrupt bytes for "git-2.54.0"
peer: fetched "git-2.54.0" from <B>:9102 (9368020 bytes, verified)
facade: served "git-2.54.0" from a peer (9368020 bytes)
```

Corrupt holder first, blacklisted, retried, **served from the swarm — upstream
was never contacted.** The caller got `86f327f30f…`.

Note the deliberate restart of C's daemon before this phase: the blacklist is
in-memory with no expiry, so restarting is the only way to clear it. That is a
property to remember when testing, and an operational fact for anyone wondering
why a peer stays skipped.

## Phase 4 — a tracker that is neither peer

A's daemon was stopped, leaving A running the tracker alone. Its registration
expired 60 seconds later, on the `Timeout` the spec fixes, and the holder set
shrank to B by itself:

```
{"peers":[{"ip":"<B>","port":9102}]}
```

C then fetched `git-2.54.0` from B at 10.9 MB/s, with the tracker on a third
machine that held nothing, served nothing and verified nothing:

```
[C]  peer: fetched "git-2.54.0" from <B>:9102 (9368020 bytes, verified)
[A]  tracker: query pkg="git-2.54.0" -> 1 peers
[B]  peer: served "git-2.54.0" to <C> (9368020 bytes, streamed from the cache)
```

Three machines, three roles, one transfer.

## Phase 5 — ADR-002's `503`, unplanned but cheap

Not on §3.4's list, and never fired outside a unit test. B was restarted with
`max_concurrent_seeds: 1` and C asked for two packages at once:

```
[B]  peer: 503 for <C>: the global max_concurrent_seeds cap of 1 is full
     (in flight: 1 total, 1 from <C>); refusing immediately, no queueing
[C]  facade: "git-2.54.0": 1 peer(s) tried, none served a verified copy … going to upstream
     facade: served "git-2.54.0" from upstream (9368020 bytes)
     peer: fetched "gcc14-14.2.0_6" from <B>:9102 (98852086 bytes, verified)
```

The 98 MB transfer held the only slot; the second request was refused instantly
rather than queued; the requester advanced and, having no other peer, took it
from upstream. **Both callers got HTTP 200.** Admission control degrades to
upstream, not to failure — which is exactly what ADR-002 claims and what the
`503`-is-not-a-`404` rule exists to protect.

## Difficulties

1. **A test that would have proved nothing.** The first hostile-peer setup left
   the honest holder in the reply and the tracker offered it first. Nothing
   would have failed; the fetch would have succeeded from the honest peer and I
   could have written "no mismatch observed" and moved on. Curating the holder
   set per phase is what made each result mean one thing. Peer order is a map
   iteration and must not be assumed.
2. **`pkill` patterns, again.** The daemons run as `./jmj`; a `-f` pattern
   naming the directory matches nothing. `pkill -x` throughout, and `ps` to
   confirm rather than trusting the exit status — the same trap recorded in the
   demo guide §2.8, hit again on the first teardown of this round.
3. **Priming two caches with the same packages.** Cached copies decay against
   the catalogue (§7.9), so the shared set had to be freshly fetched on both
   boxes; A's 20 cached packages and B's 13 had **no announceable overlap** at
   the start.

## Uncertainties

**None raised, and none silently resolved.** This round measured behaviour that
the ADRs and specs already fix; every result matched the documented design, so
there was nothing to ask about. Two things are worth flagging as observations
rather than questions:

- **A daemon will fetch a package from itself.** When a box's own cache holds
  what its facade is asked for, the tracker lists it as a holder and the fetch
  goes out over loopback and back. It is correct and it is not free. It does not
  arise during a real install — pkg does not ask a mirror for something already
  in its cache — so it is an artifact of fetching by hand, not a defect. Stated
  because someone will see it in a log and wonder.
- **One bad package disables a peer entirely until restart.** Phase 2 makes the
  cost concrete. Nothing about that contradicts the design; it is simply the
  first time it has been observable.

## Host state on exit — verified, not assumed

Read-only checks after teardown, on both boxes:

| Check | A | B |
|---|---|---|
| `pgrep -x jmj` / `trac` | 0 / 0 | 0 / 0 |
| listeners on 8080/910x | 0 | 0 |
| `/root/jmjt` | absent | absent |
| `/usr/local/etc/pkg` | absent | absent |
| repositories | stock two | stock two |
| installed packages | 18 (baseline) | 14 (baseline) |
| `/var/cache/pkg` | 40 (baseline) | 26 (baseline) |
| `pkg update` | OK | OK |

Neither box was ever configured to use jmj as a pkg repository this round — the
facade was driven directly — so `pkg`'s configuration was never touched on
either machine. The packages fetched to prime the caches (`curl`, `git`,
`gcc14`) were removed.
