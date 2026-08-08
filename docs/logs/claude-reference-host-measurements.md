# Work log — reference-host measurements and the upstream cross-check

Feature: settling the five unknowns flagged in `claude-upstream-mirror-config.md`
and the three residuals in `claude-upstream-url-key.md`, then implementing
ADR-006's advisory cross-check on what the measurements found.

The owner granted SSH access to the FreeBSD reference host. **The address is
deliberately not recorded here** — HANDOFF §7 notes this repository is public.
Ask the owner.

Host confirmed as the one HANDOFF §7 describes: FreeBSD 15.1-RELEASE-p1,
pkg 2.7.5, amd64. Everything below is read-only; nothing was installed,
configured or written, and teardown was verified rather than asserted (§8).

## What was measured

**1. `pkg config abi` — the contract `PkgABI` assumed.** Confirmed exactly:

```
$ pkg config abi          -> FreeBSD:15:amd64
exit 0, stderr empty, stdout ends in a single 0x0a
```

`strings.TrimSpace` is the right handling. This closes residual #3 from
`claude-upstream-url-key.md`.

**2. The stock repository configuration.** Three blocks in
`/etc/pkg/FreeBSD.conf`, not one:

| Repository | URL | Enabled |
|---|---|---|
| `FreeBSD-ports` | `pkg+https://pkg.FreeBSD.org/${ABI}/quarterly` | yes |
| `FreeBSD-ports-kmods` | `pkg+https://pkg.FreeBSD.org/${ABI}/kmods_quarterly_${VERSION_MINOR}` | yes |
| `FreeBSD-base` | `pkg+https://pkg.FreeBSD.org/${ABI}/base_release_${VERSION_MINOR}` | no |

Three corrections to recorded facts fell out of this:

- HANDOFF §4.5 records the block as `FreeBSD:`. It is `FreeBSD-ports:`.
- The file's header comment tells operators to disable a repository by creating
  a shadow file with `enabled: no`, *"instead of modifying or removing this
  file"*. My analysis document had argued that deleting the block is the natural
  way to disable one; FreeBSD documents the opposite, and the documented path
  preserves the URL. Corrected in place.
- `/usr/local/etc/pkg/repos/` does not exist on this host, so nothing shadows.

**3. pkg's URL variables.** `pkg.conf(5)` documents seven: `ABI`, `OSNAME`,
`RELEASE`, `VERSION_MAJOR`, `VERSION_MINOR`, `OSVERSION`, `ARCH`. The stock
config really does use `${VERSION_MINOR}`. This retires unknown #3 and, more
usefully, justifies ADR-006's refusal to proxy a URL still containing `${`:
that is not a defensive hypothetical, it is what happens if someone points jmj
at the kmods or base repository.

**4. `repodata` — the finding that changed the design.** The repository
database carries a key/value table recording where it was fetched from,
**already expanded**:

```
sqlite> select * from repodata;
packagesite|pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly
```

ADR-006 had anticipated grepping UCL for this. `repodata` is strictly better: it
is in a file the daemon **already opens read-only**, it needs no parser, it is
pre-expanded so comparison is a string match, and it records what pkg *actually
did* rather than what its config *says*. This retires unknown #4 and moots
unknowns #1 and #5 entirely.

**5. Does the repo DB carry the ABI?** (unknown #2) — **not cleanly.**
`packages.arch` splits `FreeBSD:15:amd64` (22,197 rows) against `FreeBSD:15:*`
(15,592). The wildcard form makes it a poor ABI source, so `pkg config abi`
stays correct. Recorded so nobody re-derives it hopefully.

## The cross-check, and the flaw only the host caught

Implemented in `internal/daemon/upstreamcheck.go`, advisory throughout: it warns
and never refuses, and a source it cannot read is silence.

I cross-compiled a throwaway probe (`GOOS=freebsd`), ran it against the real
catalogues, and deleted it from both the host and the repository. That run
established two things the unit tests could not.

**It works on real data.** The pure-Go SQLite driver reads the 73 MB catalogue
on FreeBSD without a C toolchain — 37,789 + 239 = 38,028 packages indexed, both
`repodata` URLs recovered, `${ABI}` expanded through the real `pkg config abi`.
Nobody had previously run any of this codebase on FreeBSD.

**And it was wrong.** My first implementation compared *per catalogue* and
warned on any disagreement. On a stock host that means a **correctly configured
daemon warns about kmods on every start**, because the second enabled repository
legitimately has a different URL. That is precisely the failure I had written a
loopback-skip to avoid two functions earlier: a warning that always fires trains
the operator to ignore the one case it exists to catch.

The fix is a change of question — from *"does every catalogue agree?"* to
*"does any catalogue agree?"*. One match means the configured value names a
repository pkg really uses. Zero matches is the genuine alarm, and the warning
then lists every repository that *was* found, which is more actionable than the
per-catalogue version ever was.

The table-driven tests could not have found this: I wrote the fixtures from the
same wrong mental model as the code. `TestUpstreamWarningsIsSilentOnAStockTwoRepositoryHost`
is the regression test, and its fixtures are the measured URLs.

Re-verified on the host after the fix: silent on a correct upstream, and one
informative warning naming both repositories on a deliberately wrong branch.

## Difficulties

**Deciding what a loopback source means.** Once the operator switches pkg to
jmj, `repodata` records *our* loopback address — so after a successful switch
the comparison has nothing real to compare against. Skipping loopback sources
means the check does its work exactly once, on the first start after switching,
while the catalogue on disk is still the one the stock config fetched. That is
the moment the mistake is made, so the timing is right, but it is worth knowing
the check goes quiet afterwards rather than continuously guarding.

**Keeping the probe out of the repository.** It needed to import `internal/`, so
it had to live in the module to cross-compile. I put it at `cmd/jmjprobe`, used
it, and deleted it — `cmd/` gains no permanent member from this work.

## Areas of uncertainty

1. **The multi-repository gap is raised, not solved.** jmj has one
   `upstream_url`; a stock host has two enabled repositories. Adopting jmj
   silently drops the others. **Raised with the owner and recorded as HANDOFF
   §4.6**, with options listed and none chosen (ground rule 3). It blocks
   nothing today.

2. **The cross-check's loopback rule is my judgement, not a ruling.** ADR-006
   says "advisory"; that a loopback source should be skipped rather than
   reported follows from what the data means, but the owner has not ruled on it.
   Low stakes — the alternative is a warning on every start after a successful
   switch — but it is a decision I made, and it is recorded here rather than
   buried in a comment.

3. **`repodata` was measured on one host, two repositories.** The same residual
   risk the path rule carries. If a third-party repository omits the `repodata`
   table, the check silently skips it, which is the designed behaviour rather
   than a failure.

4. **Still nothing consumes `upstream_url`.** Unchanged from the previous log:
   the facade is frozen, so the key is validated, expanded, cross-checked and
   then unused until §5.7.
