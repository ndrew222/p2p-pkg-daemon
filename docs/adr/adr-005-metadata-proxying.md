# ADR-005: The facade proxies pkg's repository metadata

**Status:** Approved by Andrew (ruled 2026-08-08; drafted from that ruling)

Resolves `docs/logs/HANDOFF.md` §4.4. Retires the *"the daemon never serves, caches or proxies metadata"* rule in `docs/mirror-facade-spec-v0.1.md` and UC-07, both of which are superseded by ADR-003. Evidence: `docs/logs/claude-pkg-mirror-verification.md` §7.1.

## Context

Two ratified statements contradicted each other and no ADR settled it.

- `mirror-facade-spec-v0.1.md` and UC-07 said the daemon *never serves, caches or proxies metadata*, on the grounds that the signed catalogue is the root of the integrity model and must come from a real mirror. UC-07's flow then had pkg fetch the catalogue from its **next** mirror after the facade returned an error.
- ADR-003 made jmj pkg's **only** mirror. §7.1 measured that there is no next mirror to fall through to: a facade that errors on a metadata path breaks `pkg update` outright. ADR-003 also expects the facade to forward a conditional `GET` and relay upstream's `304`, which *is* metadata proxying, and the §7 harness proxied the signed catalogue successfully — 37,789 packages, `signature_type: fingerprints` intact, followed by a real `pkg install` end to end.

So the design as it stood could not run. Refuse metadata and `pkg update` fails; proxy it and a ratified sentence is violated. ADR-003's *Decision* section rules on package files only, so this was left open under `AGENTS.md` ground rule 2 rather than resolved by reading ADR-003 generously.

**"Metadata" here means the repository catalogue files, not any hash.** Concretely, the non-package paths under ADR-004's rule: `meta.conf`, `packagesite.pkg`, `data.pkg`, directory listings, `/`. These are whole-repository documents pkg downloads during `pkg update`. It is **not** the `~hash10` suffix or the `cksum` column — those are per-package identifiers belonging to the ratified path rule, and nothing here touches them.

## Decision

**The facade proxies metadata.** A request that ADR-004's path rule classifies as a non-package request is fetched from the configured upstream mirror and relayed to pkg.

Of the two conflicting sentences, the one that gives way is *"the daemon never serves, caches or proxies metadata."* It is retired.

The one that survives is *"the signed catalogue is the root of the integrity model and must come from a real mirror"* — because **relaying is not vouching.** The bytes still originate at the real mirror, the facade adds no signature and asserts nothing about them, and pkg verifies the repository signature itself against its own fingerprints. A pass-through does not put the daemon inside the integrity model; it puts it on the path, which it already is for package bytes.

Mechanically, and consistent with ADR-003's upstream path:

- **Stream straight through. No spool, no `[]byte`.** The catalogue is large and the reference host has 1 GiB. There is nothing to withhold bytes *for*: the facade cannot verify a catalogue — the repository database carries no hash for one, and is itself the thing being updated — and there is no second source to fall back to.
- **No verification, because there is nothing to verify against and pkg does it.** This is UC-06's *"do not verify when the receiver verifies"* applied in the same direction ADR-003 applied it.
- **Relay the conditional `GET`.** Forward `If-Modified-Since` and relay upstream's `304` unmodified. This is ADR-003's stated consequence and it now has a mechanism. The facade must not synthesise a `304` from a guess — it tracks no upstream modification times, and a wrong guess serves a stale catalogue.
- **No cache.** ADR-003 already forecloses a facade-side store and that reasoning is unchanged here. Recorded so that proxying is not read as an invitation to add one.
- **Relay upstream's status.** An upstream `404` on a catalogue path is upstream's answer and reaches pkg as such; the facade does not reinterpret it. The facade emits its own error only when the upstream fetch itself fails, which is ADR-003's `502`.

### What this does not change

- **The path rule.** ADR-004 decides what counts as a package request; this ADR only decides what happens on the other branch. Nothing here loosens condition 1, which is exactly what keeps `packagesite.pkg` out of the package path.
- **`GET`-only, and the `400` for a malformed package request** under `All/`. Both stay as specified.
- **`404` for a package genuinely absent from the repository database.** Narrowed by ADR-003 and untouched here.

## Consequences

**UC-07 becomes implementable, and is rewritten.** Its steps 3 and 4 — facade returns an error, pkg falls through — are both retired. The daemon relays; there is no fall-through and no second mirror in the flow.

**`internal/daemon/facade.go`'s metadata branch is now specified, and is wrong as written.** It answers `404` to every non-package path, which §7.1 measured breaks `pkg update` outright. That branch is part of the §5.7 rework. Its tests (`facade_test.go:91`, `:189`, `:354`, and `daemon_test.go:187` which uses a metadata path as its probe) encode the refusal and must be rewritten with it — they are not evidence of correct behaviour.

**Implementation remains blocked on §4.5.** This ADR says *where* metadata comes from — the configured upstream mirror — but §4.5 has not decided how that upstream is configured, so there is still no URL to fetch from. **§4.4 is closed; §5.7 is now blocked on §4.5 alone.**

**The facade becomes a general reverse proxy for non-package paths, and the loopback constraint is what makes that acceptable.** Package-only proxying had a narrow surface; relaying arbitrary paths does not. `facade_addr` is loopback-enforced at startup precisely so the facade cannot be used as someone else's bandwidth, and that enforcement is now carrying more weight than it was. It must not be relaxed.

**Path handling needs care that package requests did not.** A package request is reduced to a validated `name-version` before anything is fetched, so the upstream URL is constructed from a known-good identifier. A proxied metadata request forwards a client-supplied path, so joining it to the upstream base URL must not permit escaping that base — `..` traversal, scheme-relative or absolute paths, or a path that resolves above the repository root. Clean and constrain before joining, and note that the request has already been through `path.Clean` for the ADR-004 rule.

**jmj is a single point of failure in front of `pkg update` as well as `pkg install`.** Not a new cost — §7.1 measured that an unreachable facade already breaks `pkg update`. This ADR stops the specs from claiming otherwise.

**The daemon now makes upstream requests that carry no P2P benefit.** Catalogue traffic is pure pass-through: it is bandwidth the daemon relays without the swarm ever reducing it. That is the price of being the only mirror, and `If-Modified-Since` relaying is what keeps it small — which is the practical reason the conditional `GET` is not optional.
