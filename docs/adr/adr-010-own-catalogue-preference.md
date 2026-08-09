# ADR-010: On a name-version collision, the daemon prefers its own repository's row

**Status:** Approved by Andrew (ruled 2026-08-10; drafted from that ruling)

Resolves `docs/logs/HANDOFF.md` §4.10. Narrows the first-in-path-order rule
recorded in `internal/daemon/repodb.go` and cited by §5.2. Evidence:
`docs/logs/claude-freebsd-host-round.md` §4.

## Context

`repo_db_dir` is scanned for every catalogue on the host, and `Repositories`
indexes them into one map keyed by name-version. When two catalogues carry the
same name-version, the first in sorted path order wins and the collision is
logged. That rule was ratified on a measurement: **zero collisions** across both
repositories on the reference host, 37,835 and 239 rows.

The host round invalidated the premise, and it did so by doing the ordinary
thing. **Configuring jmj as a pkg repository makes pkg create
`/var/db/pkg/repos/jmj/db` — inside the directory jmj scans.** Measured, with
the stock repositories left enabled, which is ADR-007's coexistence:

```
daemon: loaded 37813 package(s) from /var/db/pkg/repos/jmj/db
daemon: 37813 name-version(s) appear in more than one repository;
        the first in path order won: 0ad-0.28.0_4, … and 37803 more
```

So collisions are not rare; in the intended deployment **every row collides**,
because jmj's catalogue is a copy of the repository it fronts. `FreeBSD-ports`
sorts before `jmj`, so path order picks the catalogue pkg did *not* consult.

Nothing broke, because the two agreed. They can stop agreeing. jmj's catalogue
is fetched through the facade and `FreeBSD-ports`'s directly, at different
times, and §7.9 measured the repository being rebuilt with **no version bumps**:
16 of 20 cached packages had a `pkgsize` that had moved under the same
name-version. After such a rebuild the two local catalogues hold different
`cksum`/`pkgsize` for the same package.

The consequence is worse than a failed install. pkg resolves a package from the
jmj repository and asks the facade for it; the facade verifies the peer's bytes
against **`FreeBSD-ports`'s row**; the hashes differ; and *a hash mismatch
blacklists the peer*. **We would blacklist an honest peer for our own bad data.**
`repodb.go`'s own comment names this as the one consequence first-wins does not
bound.

Options considered:

1. **Do nothing, document it.** The risk is invisible until a rebuild, and its
   symptom — an honest peer dropped — is one of the hardest things in this
   system to diagnose from the outside.
2. **Report only conflicting collisions.** Necessary, and not sufficient: it
   makes the condition visible without deciding which row is used.
3. **Skip our own catalogue.** Removes the collision, but by discarding the rows
   pkg is actually acting on and keeping the ones it is not. Backwards.
4. **A config key naming the repository to ignore.** Adds a value that must
   agree with what the operator wrote in pkg's config, with no way to check it —
   a new silent misconfiguration.
5. **Prefer our own catalogue's rows.** Chosen.

## Decision

**When the same name-version appears in more than one catalogue, the row from
this daemon's own repository wins.**

The justification is not tie-breaking convenience. **pkg resolved the package
from the jmj repository**, so jmj's row is the one pkg is acting on and the one
the bytes it eventually re-verifies must match. Matching a repository pkg did
not consult is not a neutral choice between two equal candidates; it is the
wrong one.

**"Ours" is the catalogue whose recorded source is a loopback URL.** This needs
no new mechanism and no new config: `loadRepositorySource` already reads
`repodata.packagesite`, and `upstreamcheck.go` already carries both the helper
and the concept, in a comment written before this problem was found —

> A loopback source is this daemon: once the operator has switched pkg over to
> jmj, pkg records OUR address here.

Two rules keep the change contained:

- **No loopback catalogue → nothing changes.** Path order still decides. That
  covers every host that has not adopted jmj, and the first start after
  switching, before pkg has written our catalogue.
- **More than one loopback catalogue → path order between them.** Deterministic,
  as now. jmj fronts one repository (ADR-007), so this is a degenerate case, not
  a configuration to support.

**The collision log changes with it, because the change is otherwise unusable.**
A collision is currently reported whenever a name-version appears twice, which
after this ruling is the *expected* state — it would log 37,813 entries on every
reload, which is not a diagnostic but a way to teach the operator to ignore the
log. Compare the rows instead and report only genuine disagreement in `cksum` or
`pkgsize`. Silence today; a precise alarm on exactly the drift this ADR exists
to survive.

## Consequences

**The blacklist-an-honest-peer path closes.** With drift, we now verify against
the row pkg used, so a peer serving bytes that match the catalogue pkg resolved
from is accepted rather than punished.

**A conflicting-rows log line becomes worth acting on.** It means the two local
catalogues have drifted, which means one of them is stale, which the operator
fixes with `pkg update`. Recording it as a real signal is half the value of this
change.

**This narrows *which row* is used, not *whether* the package exists.** ADR-007's
trap is untouched and still applies: `repo_db_dir` holds every catalogue on the
host while jmj fronts one repository, so **a successful lookup is still not proof
`upstream_url` can serve that package.** Preferring our own catalogue makes the
row more likely to be the right one; it does not make the two predicates the
same, and §5.7's upstream non-200 branch stays.

**A third-party repository can no longer displace our row**, which is a small
security improvement in passing: an operator who adds an unrelated repository
whose catalogue happens to sort earlier can no longer change what jmj verifies
against.

**First-in-path-order survives as the fallback**, so the reasoning recorded for
it in `repodb.go` — deterministic, logged, bounded by pkg's own re-verification
— is still the rule wherever this ADR does not reach, and is not deleted.
