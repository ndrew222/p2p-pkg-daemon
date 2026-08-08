# ADR-007: jmj fronts one repository and coexists with the rest

**Status:** Approved by Andrew (ruled 2026-08-08; drafted from that ruling)

Resolves `docs/logs/HANDOFF.md` §4.6. Measurements: `docs/logs/claude-multi-repository.md`.

## Context

§4.6 recorded what looked like a hole in the model: `upstream_url` is a single URL, but a stock FreeBSD 15.1 host has two *enabled* repositories on different URLs. It concluded that adopting jmj silently drops the others, and listed three options — declare multi-repository hosts out of scope, allow a list of upstreams, or accept the loss.

**The premise was wrong, and the error was mine.** §4.6 asserted that *"ADR-003 requires jmj to be pkg's only enabled repository."* ADR-003 says no such thing. It says jmj becomes pkg's only **mirror** rather than its first — and mirrors-versus-repositories is exactly the distinction ADR-003's Context section exists to draw. Collapsing the two words invented a constraint the ADR never imposed, and then spent an open question on it.

What ADR-003 actually rejected (its option 2) was configuring a real mirror as a second repository *to act as a fall-back for the same packages*. That rejection rests on a measurement about **fetch-time retry**: a `404` from the facade does not make pkg try another repository. It says nothing against a second repository that is never asked to be a fall-back.

### Why coexistence needs no fall-through

ADR-003 already removed the need for it. Because the facade proxies upstream on a peer miss, **jmj never returns `404` for anything in its own catalogue**. There is no failure for a second repository to absorb, so the mechanism ADR-003 measured as absent is never invoked.

What multi-repository operation actually relies on is **solve-time selection**, which ADR-003 affirms in the same breath as it denies retry: for multiple repositories `man pkg-repository` promises pkg will *search* them in `PRIORITY` order. Selection is all this needs. Every package resolves to exactly one repository before any byte is fetched, and the chosen repository is then asked for a file it has.

### What the reference host actually looks like

The intuition that motivated this ruling — that ports and kmods hold disjoint package sets, so nothing can collide — is **false**, and measurably so. Measured on FreeBSD 15.1-RELEASE-p1 / pkg 2.7.5:

| Quantity | Value |
|---|---|
| `FreeBSD-ports` packages | 37,789 |
| `FreeBSD-ports-kmods` packages | 239 |
| Shared **names** | **238** |
| Shared **name-versions** | **0** |
| kmods-exclusive packages | **1** — `drm-latest-kmod` (`graphics/drm-latest-kmod`) |

The two repositories overlap almost totally by name, differing only in version:

```
wifi-firmware-mt7601u-kmod   ports: 20260410   kmods: 20251125   (both net/wifi-firmware-mt7601u-kmod)
```

Both ship at `priority: 0`, so the solver breaks the tie on version. Measured with a dry-run install:

```
wifi-firmware-mt7601u-kmod: 20260410 [FreeBSD-ports]
```

**The decisive figure is the zero, not the 238.** Name collisions are resolved by pkg before the facade is ever addressed. Name-*version* is what ADR-004's path rule keys on, and it is unique across both catalogues — so jmj holding both catalogues under `repo_db_dir` introduces no ambiguity in the one identifier the facade actually uses.

## Decision

**jmj fronts exactly one repository. It replaces that repository, and coexists with every other.**

Two rules, and the asymmetry between them is the whole ruling:

1. **The repository jmj fronts must be disabled.** jmj stands in for it; leaving both enabled means pkg holds two repositories offering identical name-versions, and whichever it picks, some fraction of requests bypass the facade. The swarm then silently receives no traffic for those packages. This is a purpose argument, not a measurement — the tie-break between two repositories offering the same name-version at the same priority was **not** measured, and the fact that it is unspecified is itself the reason not to build on it.

2. **Every other enabled repository is left exactly as it is.** They are not disabled, not proxied, and not mentioned in jmj's config. pkg continues to fetch them directly over the internet, catalogue and packages alike.

**`upstream_url` stays a single URL,** and this is now the accurate expression of the model rather than a limitation of it: jmj fronts one repository, so it has one upstream. The list-valued variant §4.6 floated is rejected — it would require the facade to decide *which* upstream a request belongs to, and ADR-004's path rule deliberately discards the repository-distinguishing part of the path, so the facade cannot make that decision even in principle.

## Consequences

**§4.6 is closed, and the config schema is unchanged.** No new key, no list, no migration. The question dissolved rather than being answered.

**Coverage is partial by design, and that is the honest cost.** Packages the solver routes to a repository jmj does not front bypass the swarm entirely — they are fetched from the internet as before, and are neither p2p-distributed nor announced as fetched. On the reference host that is `drm-latest-kmod` plus every package where the kmods version outranks the ports one. A string comparison put the latter at ~37, but string ordering is not pkg's version ordering, so **that figure is indicative and has not been measured properly.** The shape of the cost is settled; its exact size is not, and nothing depends on it.

**Priority is the operator's lever, and it is untouched.** Raising jmj's `priority` above the others makes it win every contested package and widens swarm coverage; leaving it at the default keeps stock behaviour. This ADR does not set a priority or recommend one.

**A latent inconsistency the facade rework (§5.7) must not assume away: jmj's repository-database view is broader than its upstream.** `repo_db_dir` is scanned for *every* catalogue, so `Repositories` will happily return a hash and size for a package that belongs to a repository jmj does not front — one its `upstream_url` cannot serve. Under this ADR that path is unreachable, because pkg resolves such a package to the other repository and never asks the facade for it. But "the repo DB knows it" and "upstream can serve it" are **not** the same predicate, and a facade that treats a successful hash lookup as proof the upstream will answer is relying on an invariant this ADR does not provide. Flagged, not fixed; it belongs to §5.7.

**The ADR-006 cross-check needs no change,** and its "matches none, not matches all" rule is now doubly justified. It was written to stop a correctly configured daemon warning about kmods on every start; under this ADR a host legitimately running several repositories is the expected case rather than a tolerated one.

**ADR-006's Open Question section is superseded by this ADR** and its erroneous sentence corrected in place.
