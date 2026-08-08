# ADR-002: Serving-side concurrency — a global limit and a per-remote-IP cap

**Status:** Proposed (drafted 2026-08-08 as an owner ruling by Andrew; awaiting vetting by Elroy)

Supersedes the open item at the end of `docs/logs/HANDOFF.md` §4.2, which recorded the global limit as decided and the per-remote-IP cap as raised-but-unanswered.

## Context

A seeding daemon accepts inbound `GET /pkg/<name-version>` requests and streams package bytes back. Nothing in `docs/` says how many of those it should serve at once: `peer-transfer-spec-v0.2.md` specifies the request surface, the response codes (`200`/`404`/`400`/`405`), the size bound and the timeouts, but its *Serving side obligations* section is silent on concurrency. Under `AGENTS.md` ground rule 1 — every change maps to a use-case step, a spec, or an ADR — that silence blocks the serving half of §5.3 from writing any limit at all. This ADR is the authority it needs.

Three facts frame the decision.

**Hostile peers are expected.** The owner has stated this. It is the premise; everything below follows from it.

**Nothing reclaims a held slot.** `AGENTS.md` forbids stall detectors, minimum-throughput rules and transfer deadlines, and the owner closed that question deliberately: a slow peer is out of scope exactly as a slow mirror is. The peer spec bounds only the TCP dial (5s) and the response headers (10s); the body transfer is unbounded in time by design, because a 2.83 GiB package over a domestic uplink is a legitimate transfer that no deadline can distinguish from an attack. So a slot, once taken, is held until the requester is finished or goes away. Any limit we impose is therefore a limit an adversary can pin at its ceiling.

**The file-descriptor budget is per-process.** The seeder does not have its own. It shares one with the mirror facade's outbound fetches to other peers and with the tracker keep-alive.

Options considered:

1. **No limit.** An attacker exhausting descriptors does not merely stop us seeding — it breaks the facade's outbound fetches and the keep-alive too. The daemon stops installing packages for its own user and silently drops out of the swarm. The blast radius of the seeding surface becomes the whole process.
2. **A listener-level limit** (bounded `Accept` loop, or a limited listener). Rejected: excess connections queue invisibly in the backlog until the requester's 5s dial timeout fires. Exhaustion then presents to the swarm as a stall rather than as a refusal, and the requester wastes its dial budget on a peer that was never going to answer.
3. **A global in-handler limit only.** This is what §4.2 decided. It confines the damage to seeding — the facade and the keep-alive keep their descriptors — but it does not stop one hostile IP from holding every slot, because nothing reclaims them. Necessary, and on its own insufficient.
4. **A per-remote-IP cap only.** Rejected: it bounds what any single source can take but not the process total, and the process total is the resource actually at risk.
5. **Both.** Chosen.

## Decision

**Two non-blocking semaphores in the peer HTTP handler: one global, one keyed by remote IP. If either is full, reply `503` immediately.** No queueing, no waiting, no `Retry-After`.

- **Remote identity is the host half of `r.RemoteAddr`**, via `net.SplitHostPort`. It is never read from a header. This matches the tracker, which does exactly this (`cmd/trac/main.go:75`) under an explicit spec rule: "the daemon's IP is always the connection's source address. It is never read from a header or body. `X-Forwarded-For` and friends are ignored" (`tracker-protocol-spec-v0.2.md:52`). A cap keyed on a client-supplied header would be a cap an attacker sets.
- **Both limits default to `0`, meaning unlimited**, and both are configurable. `AGENTS.md` asks for a real observed problem before a control of this family. The hostile-peer expectation justifies building the *mechanism*; it does not justify a *number*, and nobody has measured one. Shipping at `0` means the default behaviour is unchanged and an operator opts in.
- **Implement with §5.3, never before.** There is no `503` on the interim `peerwire` framing, and §5.3 deletes that package. A limit written against the current transport would be thrown away in the same change that makes it expressible.

### Why the usual objection to per-IP caps does not sink it here

The standing argument against per-IP caps is NAT: many legitimate clients share one public address, so a per-IP cap punishes them for their topology. That cost is real here and is **not** waved away — ADR-001 established that every daemon can fetch regardless of NAT, so an office or campus behind one address can legitimately produce several concurrent requesters against the same seeder.

What makes it acceptable is that the cost is bounded and lands on a path the design already depends on:

- A `503` is not a failure. The requester "treats every non-`200` identically: log, move to the next peer" (`peer-transfer-spec-v0.2.md`, *Responses*), so load spills to another holder with no requester-side change. `MAX_PEERS = 3` means it has up to two more to try, and behind those sits pkg's own mirror fallback — the same shortfall absorber ADR-001 already leans on for NATed peers that cannot seed. A capped-out requester degrades to a slower install, not a broken one.
- It is opt-in. At the default `0` nothing changes for anybody.
- The tracker already accepts a related limitation on the serving side: "one daemon per public IP. Two daemons behind the same NAT overwrite each other's entry" (`tracker-protocol-spec-v0.2.md:42`, pinned by `TestOneDaemonPerIP`). So a per-IP notion of a peer is not a new idea being introduced here; it is the granularity the swarm already works at.

### This is not bandwidth management

`AGENTS.md` puts throttling and bandwidth management out of scope, and reads that line carefully: it is not a ban on thinking about rate control, it records that an earlier attempt solved a problem nobody had. This mechanism is admission control, not rate control. It sets no rate, no throughput floor and no deadline; it does not slow a transfer or shape one. It answers one question at request time — is there a free slot — and if not, says so immediately and truthfully. An accepted transfer runs exactly as fast as it otherwise would.

## Consequences

**`503` must not trigger the UC-06 §5b re-announce.** That obligation attaches to `404`, where the daemon has discovered it no longer holds a package and other entries may have drifted too. A `503` means the opposite: we hold the package and are refusing to serve it right now. Re-announcing on `503` would flood the tracker precisely when the daemon is under load.

**No `Retry-After`.** The requester has other holders to try and a mirror behind them. Inviting it to wait converts a fast fall-through into a stall — the failure mode option 2 was rejected for.

**`peer-transfer-spec-v0.2.md` should gain a `503` row** in its *Responses* table ("Serving-side concurrency limit reached | `503` | try next peer") and a bullet under *Serving side obligations*. This ADR is sufficient authority for §5.3 under ground rule 1, so the spec edit is a consistency fix rather than a blocker — but until it lands, the authoritative peer-wire document does not mention a status code the implementation emits.

**The requester side needs no change.** It already falls through on any non-`200`. Nothing in this decision reaches the fetch path.

**Diagnostics must name which cap fired and for which IP.** An operator reading `503`s otherwise cannot tell an attack from a misconfigured ceiling, and the two have opposite remedies. This follows the correction applied to the §4.3 repository-database diagnostics, where a count without names left no thread back to the cause.

**A distributed attacker is not defended against.** A botnet with many source addresses defeats a per-IP cap by construction, and falls back on the global limit — which confines the damage to seeding but still lets seeding be taken away. This is an honest limit of the decision, not an oversight. Defending it would need something the design does not have and is not acquiring for MVP.

**Concurrency is not the binding constraint today, and this must not be treated as if it were.** The current seeder is byte-slice based and copies the payload twice, so a single request for the 2.83 GiB package OOMs a 1 GiB host at any limit setting. §5.3's constant-memory serving is the fix; no value of these caps substitutes for it. Do not tune them in place of doing that work.
