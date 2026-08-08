# ADR-004: The facade's package-file path rule

**Status:** Approved by Andrew

Carries forward, unchanged, the *Request surface* section of `docs/mirror-facade-spec-v0.1.md`, which is being deprecated. This ADR introduces **no new decision**. If it says anything the deprecated spec did not, that is a drafting error and the spec's text wins.

## Context

ADR-003 replaced the facade's fetch semantics — the fall-through model, the status codes, where verification happens. It did not touch the *path rule*: how the facade decides whether an incoming request is a package file at all. Grepping ADR-003 for `All/`, `Hashed` or `hash10` returns nothing.

That left the path rule specified only in `mirror-facade-spec-v0.1.md`, which the owner has now deprecated on the grounds that ADR-003 is its successor. ADR-003 is the successor for everything it covers; the path rule is the remainder.

The rule is not a loose end that can be dropped. It is:

- **Measured, not inferred.** Against FreeBSD 15.1-RELEASE-p1 / pkg 2.7.5.
- **Ratified by the owner.** The `Hashed/` level and the `~hash10` suffix were accepted after an earlier, wrong version was corrected.
- **Implemented and load-bearing.** `internal/daemon/facade.go`, `internal/daemon/watcher.go`, `internal/daemon/repodb.go` and three test files depend on it; `facade.go:9` names the deprecated spec as its contract.

Under `AGENTS.md` ground rule 1, code must map to a use-case step, a spec, or an ADR. Deprecating the spec without rehousing the rule would leave shipped code mapping to nothing.

## Decision

**The rule below is authoritative and unchanged.**

A request is a **package-file request** if and only if, after path cleaning:

1. some path segment is exactly `All`, and what follows it is either the file itself or the single segment `Hashed` and then the file, **and**
2. the last segment ends in `.pkg`, **and**
3. stripping `.pkg`, and then a trailing `~[0-9a-f]{10}`, leaves a valid `name-version` string — a final hyphen separating a non-empty name from a version that starts with a digit (the same rule the cache watcher applies to cache filenames).

Where more than one segment is `All`, the **last** one wins, so a repository that happens to be named `All` cannot displace the real one.

The package identifier is the last segment with `.pkg` and any `~hash10` removed — `gopls-0.22.0_1` from `/…/All/gopls-0.22.0_1.pkg`. **Everything before `All/` is ignored:** the repo path varies per mirror, per ABI and per branch and carries no information the daemon needs. The daemon matches on the tail, not the prefix.

Anything failing the rule is a **non-package request**: `meta.conf`, `packagesite.pkg`, `data.pkg`, directory listings, `/`, and anything else. Note that `packagesite.pkg` ends in `.pkg` but does not sit under `All/`, which is precisely why condition 1 is load-bearing.

### Why `Hashed/` and `~hash10`

Both were measured, not inferred. `pkg -d fetch -y -o /tmp/jmjprobe indexinfo` requests:

```
/…/All/Hashed/indexinfo-0.3.1_1~ae9dce33aa.pkg
```

and the repository database's `path` column agrees: `All/Hashed/<name>-<version>~<hash10>.pkg`, where `hash10` is the first 10 characters of `cksum`.

An earlier revision required `All` to be the *second-to-last* segment. Every real fetch from pkg 2.7.5 therefore failed condition 1, was classified as metadata, and answered `404` — the daemon was a no-op against a live repository. The rule above is the ratified fix, and that history is the reason this ADR exists rather than a re-derivation: the obvious-looking version of this rule is wrong.

The suffix match is deliberately narrow — exactly ten lowercase hex digits after a tilde. A tilde is legal in a pkg version, so a looser rule would eat part of a real version string and produce an identifier no peer holds.

### Methods

`GET` only; anything else is `405`. Measured (`docs/logs/claude-pkg-mirror-verification.md` §7.3): pkg 2.7.5 issues neither `HEAD` nor `Range` against a mirror — every request across a catalogue refresh, a `pkg fetch` and a real `pkg install` was a plain `GET`. Scope that honestly: it says pkg does not use them on the paths exercised, and no observed transfer was interrupted, so resume-after-interrupt is untested and is where a `Range` would plausibly first appear.

## Consequences

**`mirror-facade-spec-v0.1.md` can be deprecated without loss.** Its fetch semantics went to ADR-003; its path rule is here. What remains in it is history — the status-code rationale that ADR-003 overruled, and resolved open questions — which is worth keeping readable but not worth obeying.

**`facade.go`'s contract comment must point here**, not at a deprecated document.

**One thing does not travel, and is deliberately left behind:** the deprecated spec's status-code table. ADR-003 rebuilt it and owns it. Nothing in this ADR should be read as reviving any part of it.

**This ADR settles no open question.** The two live ones — whether the facade proxies pkg's catalogue (`HANDOFF.md` §4.4) and how the upstream mirror is configured (§4.5) — are untouched here and remain owner decisions.

**Residual risk, inherited and unchanged:** the rule was measured on one host, one repository, one ABI. `claude-repo-db-reader.md` has since checked 38,074 rows across both repositories without a counterexample, which narrows it but does not close it.
