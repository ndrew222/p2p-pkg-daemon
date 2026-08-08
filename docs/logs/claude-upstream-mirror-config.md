# How the upstream mirror is configured — tradeoff analysis for HANDOFF §4.5

**Status: analysis for an owner ruling. Nothing here is decided.** Written at
the owner's request on 2026-08-08, after the §4.4 ruling (ADR-005). It gives a
recommendation at the end because one was asked for; the recommendation is not
an implementation licence, and `AGENTS.md` ground rule 3 still applies to the
key name, the TLS decision and the choice of mirror.

## Why this got bigger than it was

§4.5 was scoped as "ADR-003 needs an upstream for package misses." **ADR-005
changed what the setting means.** The facade now proxies the catalogue too, so
the upstream URL no longer names a *fallback source* — it names **the repository
pkg actually gets**.

That is worth stating plainly, because it is the fact most likely to be missed:

> pkg's own config points the jmj repository at `facade_addr`, which is
> loopback. Nothing in pkg's config says which real repository that is. The
> identity of the repository — branch, ABI, mirror — is now determined entirely
> by jmj's upstream setting.

The consequence is a failure mode neither option removes by itself. If the
upstream is set to `quarterly` while the operator believed they were on
`latest`, **nothing errors.** pkg fetches the catalogue through jmj from
quarterly, populates its repository database from quarterly, requests quarterly
packages, and every hash matches, because the whole system is self-consistent —
just not the repository the operator thought they configured. The FreeBSD
fingerprints sign both branches, so signature verification does not catch it
either. The only symptom is package versions being subtly wrong.

Any ruling here should say what, if anything, detects that.

## The options

### A — Explicit config key, required

A new key in jmj's config (name undecided — ground rule 3). Startup fails if it
is absent or unparseable.

**For:**

- **Trivial to implement and to test.** It is a string and a `url.Parse`. The
  tests run in the gate on any OS with an `httptest` server, which matters
  because there is no FreeBSD CI and the gate is the only gate.
- **The setting that now defines the repository is stated, in one place, by the
  operator.** Given the section above, that is an argument on the merits and not
  just a preference for explicitness.
- **No coupling to a file we do not own.** pkg's config format, its merge order
  and its variable set can all change without breaking jmj.
- **Fits `-generate-config` unchanged.** UC-01 step 2 guarantees generation
  touches no filesystem, so a config can be generated on one machine for
  another. A literal string honours that; discovery cannot participate in
  generation at all (see below).

**Against:**

- **One more thing to get wrong,** and the branch/ABI mismatch above is exactly
  the way it goes wrong — silently, self-consistently.
- **Duplicates a URL already on the machine**, which invites drift: the operator
  changes pkg's repo config later and jmj keeps proxying the old target.

### B — Discover from pkg's own config

Read `/etc/pkg/FreeBSD.conf` (and, to be correct, `/usr/local/etc/pkg/repos/*`),
take the stock repository's `url`, strip `pkg+`, expand `${ABI}`.

**For:**

- **Cannot drift and cannot mismatch the branch,** because the URL comes from
  the same statement that would have told pkg what to fetch. This directly
  addresses the failure mode above, and it is discovery's real argument — not
  the convenience.
- **Read-only, and consistent with existing precedent.** The daemon already
  reads pkg's repository database. Reading another pkg-owned file breaks no
  constraint.
- **Zero-config happy path** — but see the first item against.

**Against:**

- **The zero-config benefit is much smaller than it looks.** Under ADR-003 jmj
  must be pkg's only *enabled* repository, so before any of this works the
  operator must already add a jmj repo block and disable the stock one — and
  HANDOFF §8 records that getting that wrong leaves the host with no working
  package manager. They are already editing pkg's repo config carefully, and
  already generating a jmj config. One more key is a marginal cost near zero.
  Discovery does not save a step; it saves a line.
- **It couples jmj to *how* the operator disables the stock repo.** `enabled: no`
  leaves the URL readable and discovery works. **Deleting the block — the more
  natural way to disable something — leaves nothing to find.** So does renaming
  the repo. Discovery turns a reasonable operator action into a jmj failure,
  and that requirement lives nowhere in pkg's documentation.
- **Faithful discovery is not "read a file."** Per the HANDOFF §8 trap,
  `/usr/local/etc/pkg/repos/` is read *after* `/etc/pkg/FreeBSD.conf` and a
  block **shadows by name**. To find the URL pkg would actually use, jmj has to
  replicate pkg's multi-file merge and shadowing semantics. Reading only
  `/etc/pkg/FreeBSD.conf` gets the wrong answer for exactly the operators who
  have customised their mirror — the ones most likely to care.
- **The file is UCL, not JSON.** From the snippet HANDOFF §4.5 itself records —
  `FreeBSD: { url: "...", enabled: no }` — it has unquoted keys, bare-word
  booleans and no top-level braces. `encoding/json` will not read it. So this
  costs either a dependency or a hand-rolled subset parser for a format we do
  not own, and a subset parser is a bet that no real config uses the parts we
  skipped.
- **`${ABI}` expansion needs an ABI from somewhere.** `pkg config abi` reports
  it, but that means **executing the pkg binary**, which is a new integration
  surface: `AGENTS.md` says the integration surface is mirror HTTP, and reading
  pkg's *files* is the established precedent, not running it. Whether shelling
  out counts as "wrapping" pkg is an owner call, not mine.
- **Its tests are weakest exactly where its risk is highest.** You can inject a
  root path and test the parser against fixtures, but the fixtures encode *our
  model* of pkg's config semantics — and that model is the thing most likely to
  be wrong. The gate cannot test it against real pkg on any developer machine.

### C — Hybrid: explicit key overrides, discovery is the default

HANDOFF §4.5 sketches this, with a hard startup failure when neither yields a
URL.

**For:** the zero-config path when it works, an escape hatch when it does not.

**Against:** it pays **all** of B's implementation and fragility cost — the
parser, the merge semantics, the ABI expansion, the untestability — to make a
path that A already covers optional. And it adds an ambiguity of its own: when
jmj proxies from an unexpected mirror, the operator's first question is "where
did that come from?", and under C the answer depends on whether a key was set.
A and B each have one answer to that question; C has two.

## Comparison

| | A — explicit key | B — discovery | C — hybrid |
|---|---|---|---|
| Implementation | a string and `url.Parse` | UCL parse + merge/shadow semantics + `${ABI}` | B, plus precedence |
| Testable in the gate on any OS | yes, fully | only against our own model of pkg | only against our own model |
| Survives pkg changing its config format | yes | no | degrades to A |
| Branch/ABI mismatch possible | **yes, silently** | no | yes, when the key is set |
| Breaks if operator deletes the stock block | no | **yes** | no |
| Breaks if operator overrides the repo in `repos/*.conf` | no (stale, not broken) | yes, unless merge semantics are replicated | as B, until overridden |
| Needs to execute the pkg binary | no | probably, for `${ABI}` | probably |
| Where an operator looks to answer "why that mirror?" | one key | pkg's config, merged | depends |

## Recommendation — for the owner to accept or reject

**A, plus a best-effort advisory cross-check.**

Require the explicit key. Then, at startup, *try* to read pkg's config and
compare — and if the comparison fails, **log a prominent warning and carry on**;
if the file cannot be found or parsed, say nothing and carry on.

This is worth spelling out because it is not a compromise between A and B, it is
A with B's one real benefit attached at a fraction of the cost:

- It catches the silent branch/ABI mismatch, which is the strongest argument
  against A and the strongest argument for B.
- **The fragile part stops being load-bearing.** A parser that only powers a
  warning may be a crude subset parser, may fail to find the block, may ignore
  `repos/*.conf` shadowing — and the worst outcome is a missing warning, not a
  daemon that proxies from the wrong place or refuses to start. Every objection
  to B above is an objection to *depending* on that parsing.
- It can be added after the key ships, so it does not block §5.7.

If the zero-config path later proves to matter, discovery can be added as a
default without breaking anyone, because the explicit key already exists. The
reverse — shipping discovery first and retrofitting an override — is the order
that creates the precedence ambiguity.

**Three things I am explicitly not deciding**, per ground rule 3: the key's
name, whether a plaintext (`pkg+http://`) upstream is refused, and which mirror
is the recommended default. On TLS, one observation for whoever rules: because
pkg verifies the catalogue signature and jmj verifies package hashes against the
repository database, a plaintext upstream is a privacy and tamper-*detection*
question, not an integrity hole — tampering is caught either way. That argues
for defaulting to `https` and warning on `http`, rather than refusing it.

## MEASURED, 2026-08-08 — the list below is now settled

The owner granted SSH access to the reference host after this document was
written, and every item was checked. **Results first; the original list follows
unchanged so the reasoning stays legible.**

| # | Question | Answer |
|---|---|---|
| 1 | A maintained Go UCL parser? | **Moot.** Item 4 removed the need to parse UCL at all. |
| 2 | Does the repo DB carry the ABI? | **Not cleanly.** `packages.arch` holds `FreeBSD:15:amd64` for 22,197 rows but `FreeBSD:15:*` for 15,592 — the wildcard form makes it a bad ABI source. `pkg config abi` stays the right answer, and it is permitted. |
| 3 | Full set of pkg URL variables? | **Seven**, per `pkg.conf(5)`: `ABI`, `OSNAME`, `RELEASE`, `VERSION_MAJOR`, `VERSION_MINOR`, `OSVERSION`, `ARCH`. The stock config uses `${VERSION_MINOR}` for kmods and base. |
| 4 | Do `meta.conf`/the catalogue record their source URL? | **YES, and this is the useful finding.** The repository database has a `repodata` key/value table holding `packagesite` → the **already-expanded** upstream URL. No UCL parsing needed for the cross-check; see ADR-006. |
| 5 | Exact merge/shadow order across repo config locations? | **Documented and moot for us.** `pkg.conf(5)`: files are read in search-path order, later ones override earlier. Irrelevant now that item 4 supplies the URL directly. |

**One correction to this document's own argument.** Under *Against*, I wrote that
deleting the stock block is "the more natural way to disable something". The
stock `/etc/pkg/FreeBSD.conf` opens with a comment explicitly telling operators
the opposite — *"To disable a repository, instead of modifying or removing this
file, create a `/usr/local/etc/pkg/repos/FreeBSD.conf`"* with `enabled: no`. The
documented path therefore **preserves** the URL. That weakens my objection: an
operator following FreeBSD's own instructions leaves discovery something to
find. It does not change the decision, which rested mainly on the other
objections and on the small size of the benefit — and item 4 has since made the
whole discovery-versus-key question moot for the cross-check, which was
discovery's strongest use.

**A second correction, to HANDOFF §4.5's recorded snippet.** It names the stock
repository block `FreeBSD:`. The actual name is **`FreeBSD-ports:`**, and there
are three blocks, two of them enabled.

## What I did not verify — and how to settle it cheaply

*(Original text, retained. All five are answered above.)*

I have no FreeBSD host; the owner does (HANDOFF §7). None of these blocks the
ruling, but each would sharpen it, and I am flagging them rather than asserting
past them — HANDOFF §8, *do not claim a fact about a system you have not
inspected*.

1. **Is there a maintained Go UCL parser?** I did not find one. If a good one
   exists, B's parsing objection weakens considerably (its merge-semantics and
   `${ABI}` objections do not).
2. **Does the repository database carry the ABI?** If it does, `${ABI}` expands
   without executing pkg, which removes B's integration-surface objection.
   Cheap to check on the host against a catalogue under `repo_db_dir`.
3. **What is the full set of URL variables pkg expands?** I know `${ABI}` from
   HANDOFF's recorded snippet. If pkg also expands others in repo URLs, a
   discovery implementation that handles only `${ABI}` is incomplete in a way
   that will not show up on the reference host. `man pkg.conf` settles it.
4. **Do `meta.conf` or the catalogue record their source URL or branch?** If so,
   the advisory cross-check above can be made against pkg's *own data* rather
   than against pkg's config file, which would be strictly better — no UCL
   parsing at all.
5. **Exact merge and shadow order across pkg's repo config locations.** One
   instance is recorded in HANDOFF §8; I have not seen the general rule, and B
   depends on it being replicated correctly.

## Uncertainties I raised rather than resolved

Per `AGENTS.md` ground rule 2 and the work-log requirement:

- **The whole of §4.5 is unresolved and stays that way.** This document
  deliberately stops at a recommendation. No key name has been invented, and no
  code has been written against any of the three options.
- **Whether executing `pkg config abi` violates "pkg is never wrapped."** Raised
  here, not resolved. It is a constraint-interpretation question and it belongs
  to the owner. It only binds under B or C.
- **Whether the silent branch/ABI mismatch deserves its own hard check** rather
  than the advisory warning I recommend. I lean advisory because a hard check
  needs a trustworthy source for the comparison and item 4 above is unverified,
  but a stricter reading is defensible and is the owner's to take.
