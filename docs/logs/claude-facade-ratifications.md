# Work log — ratifying §4.8's four judgement calls

**Author:** Claude (Opus 5), 2026-08-09
**Governing documents:** `docs/adr/adr-009-facade-status-set.md` (new, Approved),
ADR-003, ADR-005. Origin: `docs/logs/claude-facade-rework.md`.

## What this was

The §5.7 facade rework made four calls where the ADRs settle the rule and leave a
mechanism detail unstated. It recorded them at HANDOFF §4.8 rather than
inheriting them silently. The owner ruled on all four on 2026-08-09.

| # | Call | Ruling | Cost |
|---|---|---|---|
| a | `ErrSpool` → upstream, no `500` | **Upheld**, ADR-009 written to make it binding | comment fix in two files, one new test |
| b | query string relayed on the metadata branch | **Ratified as shipped** | none |
| c | only `If-Modified-Since` forwarded | **Overturned** — `User-Agent` relayed too | one code change, one test |
| d | transparent gzip disabled | **Ratified as shipped** | none |

Recording them was not a formality: one of the four was overturned, and it was
the one whose stated cost — "mirror operators see Go's default user-agent rather
than pkg's, which matters if anyone is counting clients" — was concrete enough
for the owner to weigh.

## (a) — upheld, and given an ADR

This was the only one of the four that was a *disagreement* rather than an
unstated mechanism: `internal/peer`'s `ErrSpool` doc claimed the facade answers
`5xx`, and the reworked `facade.go` sends it to upstream. Two shipped components,
opposite readings, neither an ADR.

The ruling upholds `facade.go`. Three things were needed to make that stick, and
the third is the one that matters:

1. `internal/peer/fetch.go` — the comment keeps its real reason (the fetch loop
   stops rather than blaming every holder for this daemon's fault) and drops the
   claim about the caller's status code.
2. `internal/daemon/facade.go` — the header cites ADR-009 instead of flagging an
   open question.
3. **A test.** The behaviour was previously visible only as prose in two files,
   one of which said the opposite, so either could be "fixed" to match the other
   by someone who did not know a choice had been made.
   `TestFacadeUnwritableTempDirGoesToUpstream` already covered the `200`;
   `TestFacadeUnwritableTempDirWithNoUpstreamIs502` is new and covers the case
   where both sources are gone — `502`, never `500`.

ADR-009 states the whole status set as exhaustive rather than exempting this one
error, deliberately. "No `500` for `ErrSpool`" invites the next condition to
claim the code; "no `500`, and reopening the table needs an ADR" does not.

## (c) — overturned

The facade now relays pkg's `User-Agent` upstream. Two points of substance:

**Relayed, not hardcoded, because there are two strings.** §7.3 measured
`pkg/2.7.5` on catalogue requests and `fetch libfetch/2.0` on package fetches. A
hardcoded `pkg/2.7.5` would be wrong on every package fetch, would misreport
libfetch entirely, and would go stale with the host's pkg version. Relaying
reproduces what the mirror would have seen without jmj on the path and invents
nothing — which also means it does not need a ruling every time pkg changes.

**It crosses on the package path too**, where `If-Modified-Since` does not. The
argument that keeps the conditional `GET` off that path is that the facade must
answer a package request identically whether the bytes come from a peer or from
upstream, and a peer cannot honour a conditional `GET`. A user-agent does not
change the bytes, so that argument does not reach it, and splitting the header
across the two paths would split a mirror's client statistics for no benefit.

**Absent means absent.** When pkg sends no `User-Agent`, the facade sends none —
`req.Header["User-Agent"] = nil`, which is net/http's way of suppressing the
header. Leaving it unset would have the transport substitute
`Go-http-client/1.1`, which is exactly the string the ruling was about.

## (b) and (d) — ratified, no code

Both stand as shipped. Neither needed a change and both are now recorded as
ruled rather than as a call awaiting one.

One asymmetry is worth stating because it survives the ratification and is easy
to mistake for an oversight: **(b) covers the metadata branch only.** The
package path drops the query string (`fetchUpstream(r, r.URL.Path, "", false)`).
That is deliberate for the same reason `If-Modified-Since` does not cross there
— a package request is reduced to a validated name-version before anything is
fetched, and relaying a query on it would make the upstream request differ from
what a peer was asked for. It is now written down in §4.8(b) rather than being
inferable only from the call site.

## Difficulties

None worth the name. The work was small because §4.8 had already done the
expensive part: each call was written down with its reasoning and its cost at the
time it was made, so ruling on them was reading four paragraphs rather than
re-deriving four decisions from the code. That is the argument for the practice.

## Uncertainties

**Raised and answered this session:** all four of the above, plus the ADR
numbering — ADR-008 is the reload trigger and ADR-009 is the facade status set,
written in the order the owner ruled them.

**Not raised, because no decision was needed:** whether ADR-003 should be amended
in place rather than superseded by a new ADR. The owner chose a new ADR; ADR-003's
Approved text is untouched, and ADR-009 says explicitly that it completes that
table rather than changing it.

## Waiting on the owner

Nothing from this item. §4.8 is closed in full.
