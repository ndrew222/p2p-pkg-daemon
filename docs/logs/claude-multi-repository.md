# Work log — multi-repository hosts (§4.6 → ADR-007)

Feature: settling `docs/logs/HANDOFF.md` §4.6, which I had raised the previous
session as *"NEEDS AN OWNER RULING"*.

Measured on the FreeBSD reference host. **The address is deliberately not
recorded here** — HANDOFF §7 notes this repository is public. Ask the owner.
Everything below is read-only: `sqlite3 -readonly` with `immutable=1`, `pkg
rquery`, and one `pkg install -n -U` dry run. Catalogue timestamps verified
unchanged afterwards (May 20 / Aug 6, journals from 09:09 predating the
session), no stray `wal`/`shm`.

## How this started

The owner asked, in substance: *why can't jmj mirror the ports tree and let
kmods simply fall back to its own repository — there is no package-name
collision anyway?*

Two separable claims. The mechanism claim turned out to be right and my §4.6
turned out to be wrong. The premise about collisions turned out to be false.
Both are worth recording because the ruling survives on a different footing than
the one it was proposed on.

## The error in §4.6, which was mine

§4.6 asserted *"ADR-003 requires jmj to be pkg's **only enabled** repository."*
ADR-003 does not say that. Re-reading it against the question:

- ADR-003's Decision says *"jmj becomes pkg's only **mirror** rather than its
  first."* Mirror, not repository.
- Distinguishing the two is the entire point of its Context section: *"pkg
  distinguishes mirrors from repositories, and only mirrors get fall-through."*
- Its rejected option 2 — a real mirror as a second repository — was rejected as
  a **fall-back mechanism**, on a measurement about fetch-time retry. It says
  nothing against a second repository never asked to be a fall-back.
- Its rejected option 3 was *"ship the daemon as the sole repository and let
  misses fail"* — also rejected. So ADR-003 rejects sole-repository operation in
  the one form it considered, and nowhere mandates it.

I collapsed "mirror" into "repository" while writing §4.6, invented a constraint
from the collapse, and then spent an open question on the constraint. The lesson
is narrow and worth stating plainly: ADR-003 spends four paragraphs establishing
a vocabulary distinction, and I violated it in a document citing that same ADR.

## Why coexistence needs no fall-through

ADR-003 already removed the need for it, which is why this was never a real
hole. The facade proxies upstream on a peer miss, so **jmj never returns `404`
for anything in its own catalogue** — there is no failure for a second
repository to absorb. Multi-repository operation relies instead on **solve-time
selection**, which ADR-003 affirms in the same breath as it denies retry: pkg
*searches* repositories in `PRIORITY` order.

## Measurements

Both enabled repositories ship at `priority: 0`, `mirror_type: SRV`.

| Quantity | Value |
|---|---|
| `FreeBSD-ports` packages | 37,789 |
| `FreeBSD-ports-kmods` packages | 239 |
| Shared **names** | **238** |
| Shared **name-versions** | **0** |
| kmods-exclusive | **1** — `drm-latest-kmod` (`graphics/drm-latest-kmod`) |

So the owner's premise — no collision — is false, and nearly maximally so. The
two repositories carry the same names at different versions, same origin:

```
wifi-firmware-mt7601u-kmod   ports: 20260410   kmods: 20251125
                             both net/wifi-firmware-mt7601u-kmod
```

`pkg rquery` lists both, so the solver is the decider. Dry run:

```
$ pkg install -n -U wifi-firmware-mt7601u-kmod
	wifi-firmware-mt7601u-kmod: 20260410 [FreeBSD-ports]
```

At equal priority it takes the higher version, so on a stock host kmods is
already shadowed for all 238 shared names. Only `drm-latest-kmod` is reachable
solely through it.

**The zero is the load-bearing figure, not the 238.** Name collisions are
resolved by pkg before the facade is addressed at all. Name-*version* is what
ADR-004's path rule keys on, and it is unique across both catalogues — so jmj
scanning both under `repo_db_dir` introduces no ambiguity in the identifier the
facade actually uses. Had that number been non-zero with differing hashes, the
path rule would have had a genuine problem and the ruling would have gone
differently.

## Difficulties

**Answering the question as asked would have been wrong twice over.** The
premise was false and my own document was false, in opposite directions — the
naive replies ("no, ADR-003 forbids it" and "yes, they're disjoint") are both
wrong. Checking the collision empirically rather than reasoning from the names
is what separated them. `wifi-firmware-*-kmod` living in *ports* is not
guessable.

**Not over-claiming the tie-break.** ADR-007's rule that jmj must replace the
repository it fronts rests on a purpose argument, not a measurement: I did not
measure what pkg does with two repositories offering identical name-versions at
equal priority, because standing up jmj on the host is impossible while the
facade is frozen. The ADR says so explicitly rather than implying the rule was
measured.

## Areas of uncertainty

1. **The ~37 "kmods wins on version" figure is unsound and labelled as such.**
   It came from a SQL `>` on version strings, which is not pkg's version
   ordering. It appears in ADR-007 marked indicative. Nothing depends on it —
   it sizes a cost whose shape is already settled — but if anyone ever needs the
   real number, `pkg version -t` is the correct comparator and this figure
   should not be trusted.

2. **One host, one release, two repositories.** The same residual risk the path
   rule carries. A third-party repository could in principle collide on
   name-version with the fronted one, which is the case ADR-004's key does not
   survive. Not observed, not defended against, recorded here.

3. **Coverage loss is accepted, not solved.** Packages routed to a repository
   jmj does not front bypass the swarm. ADR-007 rules this acceptable; it is a
   real functional limit and the ADR states it in Consequences rather than
   burying it.

4. **Raised into §5.7 rather than fixed here: jmj's repo-DB view is broader than
   its upstream.** `Repositories` returns hashes for packages `upstream_url`
   cannot serve. Unreachable under ADR-007 because pkg never asks — but it is an
   invariant the facade rework could easily assume and should not.
