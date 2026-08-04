# Work log — repository database reader, interface merge, facade mounting

Covers HANDOFF §4.3 (decided), §5.2 (done), §5.4's facade half (done), and the
decision recorded for §4.2. Branch: `worktree-repo-db-reader`, off `main` at
`d01860d`.

## How I chose to tackle it

The handoff called §5.2 "the biggest unblocked win" and "the only thing between
the daemon and a real end-to-end fetch". It was not quite unblocked: §4.3 sat in
front of it, because the shape of the interface determines the shape of the
reader, and §4.3 had never been put to the owner. So the first work was a
decision brief rather than code — what each option costs, not just what each
option is.

Two things I did before writing any of it changed the answer.

**I checked the tree against the handoff instead of trusting it.** The local
checkout was on `claude/proto-v0.2`, twelve commits behind its own remote, and
the `HANDOFF.md` in it described a project state several items out of date —
§3.1, §4.1 and §5.5 had all landed on other branches and been merged. Working
from the local copy would have meant re-implementing the config schema split and
the path fixes. `origin/main` at `d01860d` was the real state.

**I measured the reference host rather than reasoning from the documents.** Four
facts came out of that, and three of them contradicted something every document
so far assumed:

| Measured | Consequence |
|---|---|
| **Two** repository databases (`FreeBSD-ports`, 37,835 rows; `FreeBSD-ports-kmods`, 239) | Every document says "the repository database", singular. The reader takes a *directory*. |
| `pkgsize INTEGER NOT NULL, cksum TEXT NOT NULL` | Hash and size are guaranteed together *by the schema*, not by convention. This is the strongest argument in the §4.3 brief. |
| 0 of 38,074 rows have a non-64-lowercase-hex `cksum`, across **both** repos | Substantially retires §7.6's residual risk, which rested on one repository. |
| Largest package 3,042,604,955 bytes (2.83 GiB) on a 1 GiB host | Reframes §4.2 entirely — see below. |

## Decisions and why

### §4.3 — composite, not a merged struct

The obvious merge (one `Lookup(nv) (RepoEntry, bool)`) protects the peer spec's
"hash and size arrive together" invariant but weakens a different guarantee:
`SanityFilter`'s signature currently *proves* the announce path cannot hash,
because it receives an interface that can only return a size. AGENTS.md cares
about that specific rule. A struct with a `Hash` field the watcher is merely
trusted to ignore trades a type-level guarantee for a comment.

`Repository` composing the two narrow interfaces keeps both. The facade holds
the composite, so hash-without-size is unrepresentable where the spec needs it;
the watcher keeps taking `RepositoryDatabase`, so its signature still states
what it may ask for. `watcher.go` and `watcher_test.go` did not change at all.

The cost is honest and worth naming: three interface names where one would do,
and one more hop to see what `Facade.Repo` can do.

### Snapshot, not live query

Neither half of `Repository` returns an `error`, and that signature is only
truthful if a lookup cannot fail. A live query hitting an I/O error would have
to report "not found", which the facade converts into a 404 — telling pkg *this
mirror does not have the package* when the truth is *this daemon is broken*.

I estimated 6-12 MB for the snapshot in the brief and then measured it against
both real catalogues: **38,074 packages in 6.3 MB**, 0.6% of the reference
host's RAM. Cheap enough that the truthful signature is clearly the better buy.

### §4.2 — a limit, and I reversed myself

My first recommendation was *no limit*, reasoning that a limit plus the
mandated-unbounded body timeout plus the ban on stall detectors hands an
attacker a cheap permanent lockout of N slots that nothing can reclaim.

The owner then said hostile peers are expected, and that exposed what I had
missed: the argument was about *who gets locked out* and ignored *blast radius*.
The fd budget is per-process. With no limit, an attacker exhausting descriptors
does not merely stop seeding — it breaks the facade's outbound fetches and the
tracker keep-alive too, so the daemon stops installing packages and drops out of
the swarm. With a limit, the damage is confined to seeding and everything else
survives. Under a hostile threat model the limit *confines*, which reverses the
conclusion. Recorded as decided in HANDOFF §4.2; the implementation waits for
§5.3, because the `503` it depends on does not exist on the `peerwire` framing
that is being deleted.

Also worth noting for whoever implements it: concurrency is not today's binding
constraint. The current seeder is byte-slice based and copies the payload twice,
so a single request for the 2.83 GiB package OOMs a 1 GiB host regardless of any
limit. §5.3's `http.ServeContent` over an open file is the actual fix.

## Difficulties

**The typed-nil trap, twice.** `New(cacheDir, d.repo, …)` with a nil
`*Repositories` produces a non-nil `RepositoryDatabase` interface holding a nil
pointer, so `SanityFilter`'s `repoDB != nil` passed and the call panicked on the
nil receiver. An existing test caught that one immediately.

The second instance was worse and I only found it because I wrote a test for the
behaviour rather than for the code: the same trap defeats `Facade.Check`, whose
whole job is to refuse to serve without a catalogue. `f.Repo == nil` was false,
`Check` returned nil, the daemon would have listened happily and then panicked on
the first package request. Both sites now go through `Daemon.repository()`, which
returns a genuinely nil interface. `TestStartHTTPServerRefusesWithoutARepositoryDatabase`
is the regression test.

The lesson I would pass on: I nearly wrote only the mounting test, which passed.
The bug lived in the negative case.

**Verifying against fixtures proves nothing about pkg.** Hand-built SQLite
fixtures test my code against my own assumptions. So I copied both real
catalogues off the host and ran the reader against them. That produced a triple
confirmation I could not have got any other way: `indexinfo-0.3.1_1` resolves to
hash `ae9dce33aa72…` and size **5905** — the `~ae9dce33aa` suffix measured in the
mirror path in §4.1(a) is the first ten characters of that hash, and 5905 is
exactly the symlink *target* size measured in §4.1(b). Three independent
measurements from different sessions agreeing.

**A diagram that contradicted the code.** I added the catalogue step to
`uc-01.puml` after the listener step, then reordered startup in `daemon.go` to
catalogue → discovery → listener (the facade needs both). Rendering the PNG and
looking at it, as §8 instructs, is what caught the contradiction.

## Areas of uncertainty

1. **Cross-repository name-version collisions.** Not specified anywhere. I
   implemented first-in-sorted-path-order wins, deterministic, with the count
   logged. **Raised with the owner in the decision brief as one of three
   candidates (first-wins / refuse to start / accept-and-log); the brief was
   approved but this rider was not answered individually, so it is
   UNRATIFIED.** Measured zero collisions across both repositories, so nothing
   turns on it today. The backstop if the wrong row wins is that verification
   fails and the peer is blacklisted — never corrupt bytes to pkg. Flagged in
   HANDOFF §4.3.

2. **Dropping malformed rows.** I drop rows whose `cksum` is not 64 lowercase
   hex or whose `pkgsize` is not positive. Not specified, and it edges toward
   the "do not write defensively for layouts we have not seen" rule from §4.1.
   I did it anyway because the failure mode is asymmetric: a malformed expected
   hash cannot match any bytes, so the fetch path would blacklist an *honest*
   peer for our own bad data. None of the 38,074 real rows was dropped. Not
   raised in advance; recorded here and in HANDOFF §4.3.

3. **Snapshot staleness.** `pkg update` rewrites these files and nothing
   triggers `Reload()` — the watcher watches the package cache, not the
   catalogue directory. A long-running daemon will go stale and start answering
   404 for packages added since startup. `Reload()` exists and is tested; wiring
   a trigger is follow-up. Not raised; recorded in HANDOFF §5.2.

4. **A reload resets the peer blacklist.** Restarting the HTTP server rebuilds
   the `Facade`, and the blacklist lives on it. I accepted this rather than
   preserving it across restarts, because the list is local, unpersisted and
   advisory, and hash verification — not the blacklist — is what makes corrupt
   bytes impossible. The cost is at most one wasted transfer per bad peer after
   a `SIGHUP`. Documented at the call site. Under the hostile-peer model the
   owner may disagree.

5. **`repo_db_dir` is a new config key I chose the shape of.** The alternative
   was hardcoding `/var/db/pkg/repos`, which would have made the reader
   untestable off FreeBSD. I followed the precedent `cache_dir` set. The key
   name and its default were not put to the owner separately; they were in the
   approved brief.

6. **Per-IP seeding cap.** Under the hostile threat model a *global* limit still
   lets one IP hold every slot, since nothing reclaims them. A per-remote-IP cap
   is what actually defends against that. It is in no spec, so per ground rule 3
   I did not build it. **Raised with the owner; awaiting a ruling.** Recorded in
   HANDOFF §4.2.

## Verification

- Gate green after every commit: `go build ./... && go vet ./... && go test ./...`,
  `gofmt -l` clean.
- Reader tested against hand-built fixtures (multi-repository, malformed rows,
  collisions, missing/empty directory, a repository directory with no `db`,
  read-only enforcement, reload) and against **both real catalogues** copied
  from the reference host: 38,074 packages, 0 dropped, 6.3 MB.
- `TestRepositoriesOpensReadOnly` asserts a `DELETE` through the DSN fails and
  that loading does not modify the file — the read-only constraint is enforced
  by the driver (`mode=ro` plus `query_only`), not by our care.
- `TestFacadeIsMountedOnFacadeAddr` brings the daemon up on a real port and
  checks the response body is the facade's, not the empty mux's stock 404.
- `docs/uc-01.puml` rendered to PNG and inspected, per §8.

**Not verified, and it is the next thing worth doing:** none of this has run
against `pkg` itself. HANDOFF §7.1 — whether pkg actually falls through to the
next mirror on a non-200 — is still the load-bearing unverified assumption of
the whole design, and the facade being mounted is what finally makes it testable.
