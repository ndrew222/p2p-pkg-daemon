# Work log — the `upstream_url` config key (ADR-006, HANDOFF §5.1 follow-on)

Feature: adding the required `upstream_url` setting that ADR-006 rules on, so
that §5.7 has a URL to fetch from. Companion to
`docs/logs/claude-upstream-mirror-config.md`, which is the analysis that fed the
ruling; this is the implementation log.

## How I chose to tackle it

The ruling arrived in four parts (key name, no default, `${ABI}` expansion,
plaintext warned not refused), and the shape of the code fell almost entirely
out of one existing split in `internal/config`:

- `ValidateFields` — values only, no filesystem, no side effects. Used by
  `-generate-config`.
- `Validate` — `ValidateFields` plus checks against *this* machine.

That split is exactly the seam the ruling needed, so the required-ness and the
URL shape went into `ValidateFields` (which is what makes the generator refuse),
and the host-specific `${ABI}` expansion went into a **new, separate**
`ExpandUpstream` rather than into `Validate`.

Keeping expansion out of `Validate` was deliberate. `Validate` does not mutate
its argument, and a function that silently rewrote a field would be a trap for
the next reader — especially one named "Validate". `ExpandUpstream` is called
explicitly at the two points where a config becomes live: `cmd/jmj` startup and
`Daemon.Reload`.

The plaintext warning could not go in `ValidateFields` either, for a sharper
reason: that function is called by the generator, whose stdout is being
redirected into a config file. A warning written there would either pollute the
config or require the function to know about stderr. So `Warnings` returns
strings and the two callers log them — the generator to stderr explicitly, the
daemon through `log`.

## Difficulties

**Two existing tests failed, and they were right to.**
`TestDefaultConfigPassesFieldValidation` and
`TestValidateFieldsIgnoresTheFilesystem` both asserted that `DefaultConfig()`
alone validates — the literal form of UC-01's "defaults are valid by
construction". ADR-006 reinterprets that invariant rather than discarding it.

I resisted the temptation to just bolt an upstream onto the default and move on.
Instead the invariant is now asserted in two halves: `defaultsWithUpstream()`
carries the "valid by construction" half, and a **new**
`TestDefaultConfigIsRefusedWithoutAnUpstream` asserts the other half — that the
bare defaults are refused *and that the error names the key*. A green test suite
that had quietly dropped the second half would have been the same failure mode
the facade tests are currently in: consistent with a retired contract.

**`${ABI}` in a URL.** I expected `url.ParseRequestURI` to reject `${` and `}`,
since they are not legal in a URI. Measured instead of assumed: Go parses it
happily as an ordinary path segment. That is what lets the placeholder pass
field validation and be resolved later, which is the whole basis of the
generate-here/expand-there split. Had it rejected, the design would have needed
a pre-parse substitution and the split would have been much uglier.

**Whether to strip `pkg+` silently.** `pkg+https://…` parses, and the scheme
comes back as `pkg+https`, so stripping it would have been two lines. I rejected
that on the codebase's own stated principle — `legacyKeys` exists precisely
because "a wrong config is reported, not quietly reinterpreted". The error names
the fix and prints the corrected URL, which costs the user nothing and does not
teach them a URL form jmj only pretends to accept.

**Guarding against expanding what we do not understand.** Expanding only
`${ABI}` and passing everything else through would let `${OSNAME}` reach an HTTP
client as a literal path segment. `ExpandUpstream` therefore refuses any
remaining `${` after expansion. This is a place where doing less than pkg does
is safe only if it is *loud*.

## Verification

Gate green: `go build ./... && go vet ./... && go test ./...`, `gofmt` clean.

The unit tests cover the URL rules, the warning, and expansion — including the
case that a literal URL must **not** invoke the ABI lookup at all, which is a
behavioural contract (the daemon has to keep working on a non-FreeBSD box) and
not merely an optimisation, so the fake records whether it was called.

Exercised the real CLI rather than trusting the unit tests alone:

- no `-upstream` → exit 1, message on stderr, **nothing on stdout**
- `-upstream 'https://…/${ABI}/quarterly'` → config emitted with the placeholder
  intact, confirming generation stays host-independent
- `-upstream 'http://…'` → warning on stderr, config still emitted
- `-upstream 'pkg+https://…'` → exit 1, message carrying the corrected URL

## Areas of uncertainty

1. **The flag name `-upstream`.** The owner ruled the *key* name (`upstream_url`)
   but not the flag. I did not treat this as a free choice: I followed the
   established mapping in this file, where `-tracker` sets `tracker_url`. So
   `-upstream` → `upstream_url` is the existing pattern rather than a new
   invention. **Raised here for correction; not clarified with the owner before
   writing it,** on the grounds that a rename is a one-line change and blocking
   on it would have stalled everything else. If the owner wants `-upstream-url`,
   say so.

2. **The advisory cross-check is specified but NOT implemented.** ADR-006
   describes it and explicitly says it does not block §5.7. I left it out on
   purpose rather than half-building it: it is the piece whose fragility must
   stay non-load-bearing, and it deserves its own change with its own tests.
   Nothing currently detects the silent branch mismatch. **Flagged as
   outstanding, not done.**

3. **`PkgABI` is untested against real pkg.** The expansion logic is fully
   tested through an injected `ABIFunc`, but the one line that actually runs
   `pkg config abi` and trims its output has never met a FreeBSD host. The
   assumed contract is: exit 0, ABI on stdout, possibly with a trailing newline.
   The owner has SSH access and this is a one-command check. Not resolved by me,
   because inventing a fallback for an output shape I have not seen would be
   worse than leaving the assumption visible.

4. **Nothing consumes `upstream_url` yet.** The facade is frozen (§5.7), so the
   key is validated, expanded and then unused. This is intentional sequencing,
   not an oversight, but it does mean the *end-to-end* correctness of the URL
   shape — specifically whether the base URL should carry a trailing path
   component that the facade appends to, and how it joins — is untested until
   §5.7. ADR-006 calls the value a "base URL"; the exact join is §5.7's to
   define, and ADR-005 already records the constraint that it must not permit
   escaping that base.
