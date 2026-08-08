# Bringing the specs into line with ADR-002 and ADR-003

Work log for the documentation pass that followed the §7 measurements. No code
changed; the gate was run anyway and passes.

## What this was

ADR-002 and ADR-003 were approved, and `AGENTS.md` puts `docs/adr/adr*.md` at
precedence rank 1 — above every spec. That left four documents describing a
model their own governing ADR had overruled, and `HANDOFF.md` §1 flagged them as
the most dangerous thing in the repository: an agent reading the map in good
faith would have built the failure model the measurement killed.

The owner asked for the stale entries to be updated.

## How I chose to tackle it

I sorted every stale item by **how much of the replacement text the ADR already
supplies**, and only wrote the ones where the answer was "all of it".

1. **The ADR states the replacement verbatim.** ADR-002's *Consequences* section
   literally dictates the `503` row and the serving-side bullet. Mechanical.
2. **The ADR states the principle and the wording follows.** ADR-003 says error
   states that ended in "pkg falls through" now end in "the facade fetches from
   upstream", and that `404`/`502` narrow. Deriving the new UC-02 flows and the
   new status table from that is derivation, not invention.
3. **The ADR does not rule.** Left alone and flagged. See *Uncertainties*.

The alternative — rewriting everything into one coherent story — would have been
faster to read and would have quietly invented the metadata rule. Ground rule 2
forbids exactly that, and the metadata question turns out to be the most
consequential thing in this pass.

I struck old text through (`~~…~~`) rather than deleting it wherever the change
was a reversal. These documents are the record of *why* the design is what it
is; a reader who finds only the new sentence learns the conclusion but not that
it was measured, and the next agent to have the original idea has nothing to
stop them. Deletion was used only where the text was merely out of date rather
than wrong.

## Difficulties

**The `Edit` tool refused multi-row table blocks in
`use-case-descriptions.md`.** Byte-identical strings — verified with `cat -A` —
would not match across several consecutive table rows, though single rows and
two-row spans matched fine. Worked around by editing one or two rows at a time.
Worth knowing: that file resists bulk edits, so budget more calls than its size
suggests.

**Deciding where the upstream fallback belongs in `uc-02.puml`.** It is entered
from three different branches (tracker unreachable, empty peer list, all peers
exhausted), and PlantUML has no `goto`. Repeating the block three times would
have tripled the thing most likely to drift. I hoisted it out of the tracker
`alt` entirely and guarded it with `opt no verified bytes from any peer`, which
is true at all three entry points and false on the happy path. Verified with
`plantuml -checkonly` and by rendering the PNG and reading it.

**UC-02's assumptions paragraph carried the load-bearing false sentence** in the
middle of a paragraph that is otherwise still correct. Replacing the paragraph
would have discarded accurate material about the size bound and NAT; so the one
sentence is struck through in place with the measurement attached.

## Areas of uncertainty

### 1. Whether the facade proxies pkg's metadata — RAISED, NOT RESOLVED

The one that matters. `mirror-facade-spec-v0.1.md` and UC-07 both say the daemon
never proxies metadata. ADR-003 makes jmj pkg's only mirror, and §7.1 measured
that a facade erroring on a metadata path breaks `pkg update` outright. ADR-003
also expects the facade to relay upstream's `304` to a conditional `GET`, which
is metadata proxying, and the §7 harness proxied the signed catalogue
successfully.

I could have resolved this myself — the ADR's intent is legible and only one
reading actually runs. I did not, because ADR-003's *Decision* section rules on
package files only, and inferring a rule of this size from a *Consequences*
paragraph is precisely the "reasonable interpretation" ground rule 2 exists to
stop. It also decides whether UC-07 survives at all.

**Raised with:** the owner, in this session, and recorded in three places so it
cannot be missed — `HANDOFF.md` §4.1, `mirror-facade-spec-v0.1.md` (open
question 7 plus an inline warning), and UC-07's description.
**Outcome:** open. Needs its own ADR.

One observation offered rather than a decision: relaying is not vouching. pkg
verifies the repository signature itself, so "the catalog comes from a real
mirror" survives a pass-through. Only "never proxies metadata" cannot.

### 2. Which upstream mirror, and the config key name — NOT ATTEMPTED

ADR-003 explicitly leaves this open. Recorded at `HANDOFF.md` §4.2 and facade
open question 6. Not invented, per ground rule 3.

### 3. A serving-side test for the ADR-002 caps — DELIBERATELY NOT ADDED

I extended the peer spec's *Definition of done* item 6 to include `503`, because
that only restates requester behaviour the spec already had ("treats every
non-`200` identically"). I did **not** add a serving-side test obligation — that
the cap fires and returns `503` — because the definition-of-done list is the
owner's test list and adding to it is a decision, not a consistency fix. Worth
the owner's attention: the mechanism is now specified but nothing tests it.

### 4. Editing `docs/` at all — FLAGGED

`AGENTS.md`'s *Layout* section says `docs/ specs — agents do not modify these`.
This pass modifies them. It was directly instructed by the owner, which
overrides, but the line now describes something that is not true and the next
agent will read it as a prohibition. Raised for the owner to settle: either
carve out "corrections that propagate an approved ADR" or keep the prohibition
absolute and route these through the owner.

## Incidental fixes

- `AGENTS.md` precedence entry 3 and ground rule 3 both pointed at
  `docs/tracker-protocol-spec-v0.1.md`, **which does not exist** — the file is
  `docs/protocol-spec-v0.1.md` (its *title* carries the `Tracker` prefix, the
  filename does not). Ground rule 3 is the rule that tells agents where the
  deliberately-unspecified tables are, so it had been pointing at nothing.
  Fixed, and it now also names `peer-transfer-spec-v0.2.md`, which has such a
  table too.
- `AGENTS.md` precedence entry 2 carried *"If this file does not exist yet, all
  wire-level code is blocked. Stop and wait."* about
  `tracker-protocol-spec-v0.2.md`, which has existed for some time. A
  stop-and-wait instruction that can now only misfire. Removed.
- Facade open questions 1 (`HEAD`) and 5 (`Range`) closed from §7.3: pkg 2.7.5
  issues neither. Both were explicitly gated on "the UC-07 integration smoke
  test", which has been run. The `Range` answer is scoped honestly in the spec —
  no observed transfer was interrupted, and resume-after-interrupt is where a
  `Range` would plausibly appear.
- The peer spec's *Deliberately unspecified* row for `HEAD` still said the
  answer depended on a smoke test that had already happened. Updated.

## Verification

- `go build ./... && go vet ./... && go test ./...` — all packages pass.
- `gofmt -l .` — clean.
- `plantuml -checkonly docs/uc-02.puml` — parses.
- `uc-02.puml` rendered to PNG and read, to confirm the new `opt` block sits
  outside the tracker `alt` and that both upstream outcomes appear.

Nothing here is executable, so the gate proves only that no code was disturbed.
