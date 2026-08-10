# Work log — the two fixes the FreeBSD host round produced (§4.9, §4.10)

**Covers** `7f295d1`, `0eaaa01` (§4.9, the ignored set) and `d6a0f76` with
`930a3c0` (§4.10, own-catalogue preference).

**Written after the fact, 2026-08-10**, by the session that noticed those
commits had shipped without one. `AGENTS.md` requires a work log per feature and
these had none: `docs/logs/claude-freebsd-host-round.md` records the
*measurements* that found both problems, and `HANDOFF.md` §4.9/§4.10 record the
*rulings*, but nothing recorded how the fixes were made or what the first
attempt got wrong.

**Sources** are the three commit messages, ADR-008's dated amendment, ADR-010,
and the code as merged. I did not run the host measurements this log refers to,
and nothing here is a new claim about the reference host — where a number
appears it is quoted from the round that measured it.

## Approach

Both items came out of the same host round and neither is a bug in the sense of
a wrong line. Each is a case where a **rule** was written deliberately, without
the measurement that would have narrowed it, and the measurement then arrived.
That shape decided how they were handled:

- Neither was fixed by the session that found it. Both were raised as HANDOFF
  items and left for the owner, because ADR-008 states the broad rule *as a
  mechanism* and ADR-010's question had no ADR at all. Narrowing a rule with
  better data is still a rule change, which is the owner's, not ours.
- Both were ruled the same way they were raised — one sentence each — and only
  then written. The diffs are small; essentially all of the risk is in the rule,
  not the code.

The two are otherwise unrelated and were kept in separate commits.

## §4.9 — which files arm the settle timer

**The problem.** `pkg update` takes `<repo>/lock` and writes `<repo>/meta`, then
goes quiet for 11.2 seconds while it downloads, and only then rewrites the
catalogue. ADR-008's two-second settle therefore fired inside that silence and
reloaded the catalogue already in memory — completely, correctly and pointlessly
— then fired again after the real rewrite. One wasted 38,052-row reload and one
spurious re-announce per update, plus a log line claiming a reload eleven
seconds before anything changed.

### The first attempt did not work, and its tests passed

`7f295d1` excluded `lock` alone. Every test passed and the commit message
claimed the fix. **Re-measuring on the host with that binary showed the symptom
unchanged**: two reloads for one `pkg update`, twenty seconds apart. pkg writes
`meta` immediately after taking the lock, so the timer was armed anyway, before
the same 11.2-second silence.

The tests were not weak in an obvious way — they checked that an excluded name
did not reload, and that it did not consume the reload a later catalogue write
was owed. They were just all *isolation* tests, and the defect was that the set
had the wrong number of members. No test that examines one name at a time can
see that.

`TestRepoWatcherReloadsOncePerUpdateSequence` is what `0eaaa01` added and what
catches it: it replays the whole measured sequence — lock, meta, silence, temp
file, rewrite — and asserts **exactly one** reload. It is an end-to-end assertion
about a real observed trace rather than a property of the ignore list, which is
the only shape that could have failed on the first attempt.

The general lesson, and the reason this log is worth writing: *the tests passing
was itself misleading evidence*, in a repository that already has a section on
exactly that (HANDOFF's note on the frozen facade — "its tests pass, which is
misleading"). A green suite meant the code did what the change intended. The
change intended the wrong thing.

### The justification changed shape between the two attempts

`7f295d1` argued from pkg's semantics: *"meta is repository metadata pkg
rewrites when the repository changes, so a reload prompted by it is defensible
even when redundant."* That reasoning is why `meta` was kept — and it is a claim
about what pkg does with its files, which is the exact species of guess ADR-008
refused to make when it said every event counts. It does not even separate the
cases: pkg writes the catalogue during an update too.

`0eaaa01` restated the rule to argue from **us**:

> `Reload` opens `<repo>/db` and nothing else — the `packages` table via
> `loadRepositoryDatabase`, the `repodata` table via `loadRepositorySource`. A
> change confined to a file we never open cannot alter the snapshot, so a reload
> owed to one is owed to nothing.

That is checkable against our own source, it is stable under anything pkg
changes about its layout, and it yields the ignored set as a consequence rather
than as a list: exactly `{lock, meta}`, because those are the only two things in
a repository directory we never read. The comment on `ignoredNames`
(`internal/daemon/repowatcher.go`) states the membership test in those terms
deliberately — *can a change confined to this file alter our snapshot?* — so the
next person to add a name has to answer that question rather than guess.

### Directories are never ignored

The one path this could plausibly have broken. A repository directory's name is
the repository's own, so it can never be in the ignored set, which means a
repository *appearing* under `repo_db_dir` still arms the timer and still gets a
watch on the re-walk. Worth stating because the obvious implementation — match
the basename and skip — would have been correct for files and silently wrong for
a new repository directory named `meta`. (Nothing names a repository `meta`; the
point is that the code should not depend on that.)

## §4.10 — whose catalogue row wins a collision

**The problem.** Configuring jmj as a pkg repository makes pkg create
`/var/db/pkg/repos/jmj/db` — inside the directory jmj scans. The daemon then
loads its own catalogue as a third repository, and with the stock repositories
left enabled (ADR-007's coexistence) every one of 37,813 name-versions collides
with `FreeBSD-ports`. Nothing broke, because both catalogues came from the same
upstream and agreed. The risk is drift: the two are fetched at different times,
a repository rebuild moves `pkgsize`/`cksum` under an unchanged name-version,
and path order would then verify pkg's bytes against the *other* repository's
row.

### It is not a tie-break, and that decided the implementation

The framing that mattered: **pkg resolved the package from the jmj repository.**
So jmj's row is the one pkg is acting on, and the one the bytes pkg re-verifies
after we hand them over must match. Using a repository pkg never consulted is
the wrong choice, not a neutral one — which rules out the other obvious option,
skipping our own catalogue, since that keeps the rows pkg did not use and
discards the ones it did.

Two things follow that are easy to want and are not available:

- **The choice cannot be delegated to pkg**, even though pkg has repository
  priority and has already picked a row before it calls us. The facade needs an
  expected hash *before* it fetches, and there is no "ask pkg" step in the
  protocol.
- **The swarm cannot disambiguate either.** The tracker announces a bare
  name-version and the peer namespace is `/pkg/<name-version>` by design, so a
  peer holding a colliding name-version has no way to say which file it has.

### Mechanically it is one reordering

`ownCatalogueFirst` puts the loopback-sourced catalogue in front of the path list
and the existing first-wins merge does the rest — no second rule, no special case
in the merge loop. Identifying "ours" needed nothing new: `upstreamcheck.go`
already reads `repodata.packagesite`, and already carried the concept in a
comment written before this problem was found — once the operator switches pkg
over to jmj, pkg records *our* address as that repository's URL, because that is
what it fetched from.

**With no loopback catalogue the order is unchanged**, so every host that has not
adopted jmj — and this one, on its first start before pkg has written our
catalogue — behaves exactly as before. `TestPathOrderStillDecidesWithoutAnOwnCatalogue`
pins that.

### The collision log changed with it, and had to

Reporting every duplicate meant **37,813 lines per reload** in the intended
deployment, which does not inform anyone; it teaches the reader to ignore the
log. The merge now compares the two rows and reports only genuine disagreement in
`cksum` or `pkgsize`. That is silent today and is a precise alarm on exactly the
drift that would otherwise have us verifying honest peer bytes against a stale
row — and blacklisting the peer for our own bad data.

The message says which catalogue won and tells the operator to run `pkg update`,
because a disagreement means one of the two is stale and that is the fix.

## Difficulties

1. **A fix that passes its tests and does not work.** Covered above; the
   correction was a test built from the measured trace rather than from the
   rule. The cost was a round trip to the host and a commit that had to be
   superseded rather than amended, since it was already merged.
2. **Choosing what a rule is allowed to depend on.** The §4.9 rewrite is
   entirely about this: an exclusion justified by our own read set is a
   different kind of statement from one justified by pkg's write set, even
   though on this host the two produce the same two names.
3. **A log line that was technically true and practically useless.** 37,813
   correct lines per reload. Worth recording because the deployment that
   produces it — jmj configured as a repository, stock repositories enabled — is
   the *intended* one, not an edge case, and nothing in the design documents
   anticipated the daemon reading its own catalogue.

## Uncertainties

Both were raised before any code was written, and both were ruled by the owner
on 2026-08-10:

| Uncertainty | Raised as | Outcome |
|---|---|---|
| May the watcher narrow ADR-008's "every event counts"? | HANDOFF §4.9, by the host round | **Ruled: ignore the files the daemon never reads.** Recorded as a dated amendment to ADR-008. |
| Which catalogue's row wins when jmj's own lands in `repo_db_dir`? | HANDOFF §4.10, by the host round | **Ruled: prefer our own.** Recorded as ADR-010 (Approved). |

Nothing was resolved silently, and neither fix was written before its ruling.

**Still true and deliberately untouched:** ADR-007's trap. §4.10 narrows *which
row* is used, not *whether the package exists*, so a successful repository-database
lookup remains no proof that `upstream_url` can serve that package.

**Re-verified while writing this log**, on the reference host, 2026-08-10, with
a freshly cross-compiled binary — see `docs/logs/claude-demo-guide.md` §2.5:

- **One reload per `pkg update -f`.** Counted across a forced catalogue refresh:
  2 reloads before, 3 after, delta 1. §4.9's fix holds on a run neither commit
  performed.
- **§4.10 visible in the reload trace.** With jmj configured as a repository,
  `/var/db/pkg/repos/jmj/db` is merged first and **no collision line appears at
  all**, though all 37,813 name-versions collide with `FreeBSD-ports` — the
  agreeing-duplicates case, silent as designed.

**Not verified by this log's author:** the numbers that describe the original
measurement — 11.2s of silence, 21,650 events, the 0.48s largest intra-rewrite
gap, and the two-reloads-per-update symptom of the failed first attempt. Those
come from `docs/logs/claude-freebsd-host-round.md` and from `0eaaa01`'s
re-measurement, and reproducing them would mean re-instrumenting the watcher.
