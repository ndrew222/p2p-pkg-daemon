# ADR-006: The upstream mirror is configured in jmj's own config

**Status:** Approved by Andrew (ruled 2026-08-08; drafted from that ruling)

Resolves `docs/logs/HANDOFF.md` §4.5, the last open decision left by ADR-003 and the last blocker on §5.7. Analysis behind it: `docs/logs/claude-upstream-mirror-config.md`.

## Context

ADR-003 requires a configured upstream mirror and deliberately left three things open: the key name, the TLS decision, and which mirror. ADR-005 then widened what the setting *is*. Because the facade now proxies the catalogue as well as package bytes, the upstream URL no longer names a fallback source — it names **the repository pkg actually gets**. pkg's own config points the jmj repository at loopback and says nothing about which real repository sits behind it.

Two mechanisms were considered in full in the analysis: an explicit key in jmj's config, or discovering the URL from pkg's own `/etc/pkg/FreeBSD.conf`. The decisive points against discovery were that its zero-config benefit is small — ADR-003 already requires the operator to disable the stock repository, so they are editing that file carefully anyway — and that faithful discovery means replicating pkg's multi-file shadowing semantics, parsing UCL, and expanding variables, none of it testable in a gate that must run on any OS.

## Decision

**The upstream mirror is dictated in jmj's config.** Discovery from pkg's configuration is **not** the source of truth.

### The key

**`upstream_url`.** It carries the base URL of a conventional mirror — the part before the repository path that pkg's own URLs also carry, e.g. `https://pkg.FreeBSD.org/${ABI}/quarterly`.

### It is required, and has no default

`upstream_url` is the one key with **no default value**. Absent or empty, the config is invalid.

This preserves UC-01's *"defaults are valid by construction"* rather than breaking it, because the invariant is enforced at the generator rather than by inventing a value:

- **`-generate-config` fails without an upstream.** It exits non-zero with nothing on stdout, so a redirect leaves an empty file rather than a config that cannot start. This is exactly the behaviour UC-01 error state 1 already specifies for an invalid setting value.
- **Therefore everything the generator emits is still valid by construction.** The generator never produces a config that will not start; it declines to produce one at all.

Inventing a default was rejected for a specific reason: the default would have had to choose a *branch* as well as a mirror, and under ADR-005 that choice silently determines which repository the operator ends up on. A wrong branch does not error — pkg populates its database from whatever jmj proxies, every hash matches, and both branches carry the same signature. A setting with that consequence should be stated, not defaulted.

### `${ABI}` is expanded, at startup, only when present

The value may contain `${ABI}`, exactly as pkg's own repository URLs do. The daemon expands it by running **`pkg config abi`**, which the owner has ruled permissible: reading pkg's files was already precedent, and executing it to ask a question is not "wrapping" it within the meaning of the constraint.

Three properties of the expansion matter and are not negotiable details:

1. **It happens at startup, never at generation time.** UC-01 step 2 guarantees the generator touches no filesystem so a config can be written on one machine for another. The generated config therefore contains the literal `${ABI}`, and the daemon resolves it against the host it actually runs on — which is UC-01 step 7's existing "validate against *this* machine" phase.
2. **`pkg` is executed only if the value actually contains `${ABI}`.** A literal URL never shells out. This keeps the daemon exercisable on a non-FreeBSD box, which `cache_dir` and `repo_db_dir` are already overridable for.
3. **Failure to resolve is fatal.** If the value needs `${ABI}` and `pkg config abi` cannot supply one, the daemon refuses to start and names the field. Proxying from a URL with an unexpanded placeholder in it is the bad outcome.

Only `${ABI}` is expanded. **Measured on the reference host, `pkg.conf(5)` documents seven variables** — `ABI`, `OSNAME`, `RELEASE`, `VERSION_MAJOR`, `VERSION_MINOR`, `OSVERSION`, `ARCH` — and the stock `/etc/pkg/FreeBSD.conf` really does use `${VERSION_MINOR}`, in the `FreeBSD-ports-kmods` and `FreeBSD-base` URLs. The repository that matters here, `FreeBSD-ports`, uses `${ABI}` alone.

That measurement is why the refusal above is not merely defensive: a config pointed at the kmods or base repository genuinely will carry a variable jmj does not expand, and it will be refused with a message naming it rather than proxied with a literal `${VERSION_MINOR}` in the path.

### Plaintext upstream is warned about, not refused

`http://` is permitted and **logged as a warning**; `https://` is expected. This is a deliberate asymmetry with `facade_addr`, which *is* refused, and the reason is that the two protect different things. A non-loopback facade is an open relay — a capability the operator cannot get back. A plaintext upstream is not an integrity hole: pkg verifies the catalogue signature and jmj verifies package hashes against the repository database, so tampering is caught either way. What plaintext costs is privacy and early detection, which is a warning-shaped problem. It also keeps a mirror-on-the-LAN setup possible.

A scheme other than `http` or `https` is rejected. A value beginning `pkg+` — the form that appears in pkg's own config, and the obvious thing to paste — is rejected with a message naming the fix, rather than silently stripped, on the same principle as the legacy-key check: a wrong config is reported, not quietly reinterpreted.

### The branch/ABI mismatch is advisory

The silent-mismatch failure mode ADR-005 created is handled by a **warning, not a hard check**. The daemon may compare its configured upstream against what pkg's own configuration says, and log a prominent warning if they disagree — but it must not refuse to start on that basis, and it must not fail if the comparison cannot be made.

The check is explicitly **best-effort, and its fragility must not become load-bearing.** If the source is absent, unreadable, or does not yield a URL, the daemon says nothing and carries on. The worst outcome of a failed comparison is a missing warning.

**The comparison source is the repository database, not pkg's config file.** This ADR originally anticipated grepping `/etc/pkg/FreeBSD.conf`, which the owner had ruled acceptable. Measured on the reference host, there is a strictly better source that needs no parsing at all: pkg records the upstream in the repository database itself, in a `repodata` key/value table, **already expanded** —

```
packagesite | pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly
```

— and that database is one the daemon already opens read-only for hashes and sizes. No UCL, no grep, no second file, and no dependence on pkg's multi-file shadowing rules. The `pkg+` prefix is stripped for comparison.

Two rules the measurement forced, both of which prevent the warning from crying wolf:

1. **A loopback source is ignored.** Once the operator has switched pkg over to jmj, pkg records *our* address in `repodata`, which says nothing about the real repository behind it. The check therefore does its work at the moment it is useful — the first start after switching, while the catalogue on disk is still the one the stock configuration fetched.
2. **The test is "matches none", not "matches all".** A stock FreeBSD 15.1 host has **two** enabled repositories with different URLs (`…/quarterly` and `…/kmods_quarterly_1`), so a per-catalogue comparison warns on every start of a *correctly* configured daemon. One catalogue agreeing is enough. See the open question below.

Implemented in `internal/daemon/upstreamcheck.go`; it does not block §5.7.

## Open question this raised — NOT decided here

**jmj has one `upstream_url`; a stock host has more than one enabled repository.** Measured: FreeBSD 15.1 ships `FreeBSD-ports` and `FreeBSD-ports-kmods` both enabled, on different URLs, plus a disabled `FreeBSD-base`. ADR-003 requires jmj to be pkg's *only enabled* repository, so configuring jmj means the other repositories stop being available — a host that used kernel modules from `FreeBSD-ports-kmods` loses them.

Nothing in ADR-003, -005 or this ADR addresses multi-repository hosts, and this ADR does not invent an answer (ground rule 3). Recorded as `HANDOFF.md` §4.6.

## Consequences

**§4.5 is closed and §5.7 is unblocked.** Both the package-miss path and the metadata path now have a URL to fetch from. The facade rework can proceed on ADR-003, ADR-004 and ADR-005.

**Existing configs become invalid.** Every config written before this key existed lacks `upstream_url` and will be refused at startup, naming the field. This is intended and is the same posture as the removed-schema check: a config missing a setting whose absence changes which repository the host installs from is a wrong config, not one to paper over with a default.

**UC-01 gains a required flag.** `-generate-config` without an upstream aborts. The use case, its diagram and `cmd/jmj/README.md` are updated accordingly.

**Reload must resolve too.** `SIGHUP` re-reads the config, so the expansion and the scheme warning belong on the reload path as well as on startup, or a reloaded config could carry an unexpanded placeholder.

**The daemon now executes an external binary, in exactly one place and for one purpose.** That is a new integration surface, permitted by the ruling and deliberately kept narrow: one command, one question, only when the placeholder is present, and never at generation time.
