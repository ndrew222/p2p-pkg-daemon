# ADR-003: Facade fetch semantics — proxy with fallback, and where verification happens

**Status:** Approved by Andrew (drafted 2026-08-08 from the §7 measurements)

Supersedes the "every failure becomes an HTTP error, which makes pkg fall through to its next configured mirror" assumption in `docs/use-case-descriptions.md` UC-02, and the status-code table in `docs/mirror-facade-spec-v0.1.md` that encodes it. Evidence: `docs/logs/claude-pkg-mirror-verification.md`.

## Context

The facade's entire failure model rests on one sentence in UC-02: *"The 'fall back to mirror' outcomes are plain HTTP errors — pkg's native mirror fallback does the rest, so pkg is never modified."* Every failure path in UC-02, the whole of UC-07, and the facade's status-code table depend on it. It had never been tested. HANDOFF §7.1 flagged it as the load-bearing assumption of the design and said that if it were false, the architecture changes.

It is false as the design relies on it. Measured on pkg 2.7.5 / FreeBSD 15.1-RELEASE-p1:

**pkg distinguishes mirrors from repositories, and only mirrors get fall-through.** `man pkg-repository` §REPOSITORY MIRRORING says of an HTTP mirror list: *"Mirrors are tried in the order listed until a download succeeds."* That is exactly the semantic jmj needs. For multiple *repositories* the same page promises only that pkg will **search** them in `PRIORITY` order. Search is solve-time selection; it is not fetch-time retry. jmj is configured as a repository. It gets selection, not retry.

**So a facade error ends the install.** With a healthy `FreeBSD-ports` configured alongside and holding the package, a `404` from the facade fails `pkg install` with exit 1 and no attempt at the other repository. `503` and connection-refused behave identically. Corrupt bytes (§7.4) are caught as a checksum failure and likewise go nowhere.

**The escape route is closed.** Of the three ways to obtain an ordered mirror list, `mirror_type: srv` needs control of DNS and cannot express a loopback daemon from `repos/*.conf`; `mirror_type: http` is the documented mechanism that fits jmj exactly and **segfaults pkg 2.7.5**; two repositories with `priority` gives the selection-without-fall-through above. None delivers daemon-first, mirror-second today.

**But the facade model itself works.** A proxy that served the signed catalogue from a real mirror and intercepted only package files was accepted by pkg as a genuine repository: `pkg update -f` processed **37,789 packages** with `signature_type: fingerprints` intact, and a real `pkg install` completed end to end including a dependency. The harness worked *because* it proxied upstream instead of answering `404`. That is the finding this ADR acts on.

Two further measurements bear on the mechanism: pkg issues only plain `GET` — no `HEAD`, no `Range` (§7.3) — and it issues conditional `GET`s carrying `If-Modified-Since` for catalogue files, which the facade has no answer for today.

Options considered:

1. **Keep the `404` design and wait for pkg.** Rejected: it depends on an upstream fix to `mirror_type: http` that nobody has filed, let alone landed, and the design is broken in the meantime.
2. **Require operators to configure a real mirror as a second repository.** Rejected: measured not to work. That is precisely the configuration that fails above.
3. **Ship the daemon as the sole repository and let misses fail.** Rejected: a peer miss is the common case, not the exceptional one. This makes jmj strictly worse than no jmj.
4. **Facade proxies to a real mirror on a peer miss.** Chosen.

## Decision

**On a peer miss the facade fetches the package from a configured upstream mirror and serves those bytes, rather than returning an error.** jmj becomes pkg's *only* mirror rather than its first. `404` stops meaning "go elsewhere", because there is no elsewhere.

**Verification placement is asymmetric, and the asymmetry is the point:**

- **Peer path — spool and verify before serving.** Unchanged from UC-02 §8–10: stream into `temp_dir`, bound by the expected size, hash incrementally, serve only on a match.
- **Upstream path — stream straight through.** No spool. Hash incrementally for diagnostics, but do not withhold bytes pending the result.

### Why the buffer is not needed on the upstream path

UC-02 and UC-01 both justify the temp directory the same way: *"verification needs the whole file before any byte may reach pkg."* Examined against what it actually buys, that rule is right for one path and vacuous for the other.

Integrity does not come from the buffer. It comes from pkg, which re-verifies every byte it is handed — UC-02 §10, *"pkg re-verifies, writes the file to its own cache, and installs"* — against the same signed catalogue the facade reads. What the buffer really buys is the ability to **abandon a bad source and try another without pkg ever seeing a failure**, because once the first byte of a `200` body is written the response is committed.

On the peer path that is worth having: `MAX_PEERS = 3`, so a hostile peer costs a retry rather than a failed install, and the blacklist (§5.5) needs the verdict anyway.

On the upstream path there is no next source. Upstream is the terminal fallback. If its bytes are bad, spooling yields a `502` and a failed install; streaming yields bytes that pkg rejects and a failed install. The outcome is identical and the spool costs 2.83 GiB of disk before the first byte moves, doubled I/O on every miss, and temp space sized to the largest package in the repository.

**The codebase already ratifies this reasoning in the other direction.** UC-06's serving side does not hash at all: *"No hash is computed on this side; the requester verifies against its own repository database."* "Do not verify when the receiver verifies" is existing, accepted design here. pkg is a receiver that verifies. Applying the same rule to the facade→pkg direction is consistent with the specs' own logic rather than an exception to it.

Two things make streaming safe in practice. `Content-Length` is known before the transfer starts, because §5.2's repository-database reader supplies the exact `pkgsize` from the same row as the hash — so the facade commits to a correct length rather than guessing. And a mid-transfer upstream failure produces a short body against a promised length, which is **exactly what pkg sees from a real mirror having a bad day** and handles routinely. It is not a new failure mode the design has to absorb.

## Consequences

**UC-02's failure paths are rewritten.** Error states that ended in "pkg falls through to its next mirror" now end in "the facade fetches from upstream". The `502` in the facade status table narrows to mean *peers and upstream both failed*, which is the only remaining case where the facade has nothing to serve.

**`404` changes meaning and mostly disappears.** It stops being the fall-through signal and remains only for a package genuinely absent from the repository database — a request the facade can prove is unanswerable. UC-06 §5b's re-announce obligation attaches to the *peer* wire's `404` and is untouched by this.

**A new config key for the upstream mirror**, and with it TLS to that mirror and a decision about which mirror. `pkg+https://pkg.FreeBSD.org/${ABI}/quarterly` resolves via SRV, which the daemon would have to either resolve itself or sidestep with a concrete host.

**`temp_dir` keeps its consumer.** The peer path still spools, so the field is not the validated-and-unused setting HANDOFF §3.1 worried about. Its justification narrows from "verification needs the whole file" to "retry needs the whole file", which is the honest version.

**`If-Modified-Since` is answered for free.** Proxying lets the facade forward the conditional `GET` and relay upstream's `304`, which is the correct behaviour and one the facade cannot produce on its own — it tracks no upstream modification times. A facade that ignored the header would waste catalogue bandwidth on every `pkg update`; one that answered `304` from its own guess would serve a stale catalogue.

**jmj becomes a single point of failure in front of pkg — and this is not a new cost.** §7.1 measured that an unreachable facade already breaks `pkg update` outright; it does not degrade. The design has had this property since the facade was mounted. This ADR stops the specs from claiming otherwise.

**Streaming must not reintroduce a `[]byte`.** `AGENTS.md` forbids holding a package in memory on either end of a peer transfer, and the same discipline binds here for the same reason: the largest package is 2.83 GiB and the reference host has 1 GiB. `FetchFromPeer` returning a `[]byte` is already the blocker HANDOFF §3.1 identified; §5.3 fixes it, and the upstream path must be written the same way from the start.

**This does not settle whether to pursue the pkg bug.** If `mirror_type: http` were fixed upstream, mirror-list fallback would become available and the `404` design would work as originally specified. That is a reason to file the bug, not a reason to wait for it: depending on an upstream fix landing is a worse bet than owning the path. A draft report exists.

**The facade does not cache what it proxies, and this is not a new decision.** The bytes pass through it, so it plainly could — but a facade-side cache is a daemon-owned store, and the design already forecloses one in three places: `AGENTS.md`'s hard constraints ("the daemon writes only to its own temp buffer directory"), UC-02's assumptions ("the daemon has no package store of its own"), and UC-06's ("there is no daemon-owned store to poll"). Recorded here only so that streaming does not read as an invitation to add one.

It would also be redundant and self-defeating. Redundant because the bytes are already retained: UC-02 §10 has pkg write what the facade serves it into `/var/cache/pkg`, which is the directory the daemon seeds from, so proxied packages join the swarm exactly as peer-fetched ones do — a facade cache would be a second copy of the same bytes on the same disk. Self-defeating because writing them is precisely the I/O that streaming just eliminated; a cache reinstates the cost the decision above removed, to hold something already held.

**The one case a store would serve is out of scope, not overlooked.** UC-05 notes that `pkg clean` empties the seed source and so deregisters the daemon until a new package appears. A daemon-owned store would survive that. That is a request for the store the design refuses, and belongs in its own ADR against `AGENTS.md`'s constraint — not smuggled in as a side effect of a fetch-semantics change.
