# ADR-001: Transport & NAT traversal — punt

**Status:** Approved

## Context

Peer-to-peer transfers require the requesting daemon to open a connection to a serving daemon. Consumer NAT blocks unsolicited inbound connections, so a serving daemon behind NAT is unreachable unless something is done. Options considered:

1. **UDP hole punching (with QUIC on top)** — highest peer coverage; requires tracker-coordinated rendezvous machinery on both ends, a UDP-based transport, and its own failure modes. Substantial implementation and testing surface for a project due in August.
2. **TCP simultaneous open** — flakier than UDP punching in practice; previously deprioritised.
3. **Relay via tracker** — rejected outright: the tracker performs peer lookup only and never relays data (standing architectural constraint).
4. **Punt** — no NAT traversal at all. Plain HTTP over TCP to the peer's advertised IP:port.

## Decision

Punt. Peer transfers are plain HTTP over TCP, addressed to the IP:port the serving daemon advertised. No hole punching, no rendezvous, no UDP.

- Every daemon can **fetch** (outbound connections traverse any NAT).
- Only directly reachable daemons — public IP, manual port-forward, or UPnP mapping — can **serve**.
- Peers behind restrictive NATs never successfully seed; the mirror fallback absorbs the shortfall. Nothing breaks; the network is smaller than it could be.

QUIC with UDP hole punching is the **stretch goal**, not the plan.

## Consequences

**Protocol spec detail — serving port in the announce.** The tracker learns a peer's IP from the connection's source address, but the listening port cannot be inferred and must travel in the announce message: `announce(listeningPort, packageList)`. The tracker's table is keyed IP:port → package list. (Reflected in UC-05 and `uc-05.puml`.)

**Dead-peer handling is free.** A NATed daemon will still announce and appear available while being unreachable. Mitigation: the requester's connection timeout advances to the next peer — which UC-02's retry loop already does (error state 5 / flow e). No blacklist entry, no tracker involvement.

**Security side effect (see B25).** Running announces over TCP means an attacker cannot overwrite a victim's tracker entry with a spoofed source IP — the handshake must complete. The spoofing avenue that made A14's overwrite-by-known-IP marginally risky is closed by this decision.

**No rendezvous machinery in the base design.** The tracker's role is discovery only. UC-08 (rendezvous sequence diagram) is shelved; `uc-06.puml`'s direct-request model is correct as-is.

**Reduced seeding population.** The cost. Unmeasured, but bounded below by the population of technically inclined FreeBSD users with public IPs or the ability to forward a port — plausibly a meaningful fraction of the target audience, and the GhostBSD/Storj-style bandwidth relief only requires *some* seeders, not all.

**Stretch-goal scoping.** If QUIC hole punching is attempted later, it needs: (a) a UDP transport with the tracker as rendezvous coordinator (UC-08, new sequence diagram), (b) tracker messages to introduce requester and server to each other, (c) the security analysis in B25 revisited, since UDP reopens the spoofed-announce question. The base design deliberately leaves the tracker's message set small so this can be added without breaking existing peers.

**Presentation framing.** State the limitation as a scoping decision, honestly: peer coverage was traded for implementation certainty, with a clear, already-scoped upgrade path.
