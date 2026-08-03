# Mirror Facade Spec — v0.1

*The pkg↔daemon HTTP surface (UC-02, UC-07). This is a separate wire from the
tracker protocol: `tracker-protocol-spec-v0.2.md` governs daemon↔tracker and
says nothing about this surface.*

**Status: drafted by an implementing agent at the spec owner's instruction, not
by the spec owner.** The path rule below is derived from one worked example
supplied by the owner; the status codes were chosen by the implementer because
the use cases specify only "an HTTP error". Both are open to revision — treat
this as a record of what the code does, not as a ratified contract.

## What the facade is

The daemon is configured as pkg's **first mirror**. pkg — never the user —
makes ordinary HTTP mirror requests to it. The facade answers exactly one kind
of request (a package file it can fetch from a peer and verify) and returns an
HTTP error for everything else, which makes pkg fall through to its next
configured mirror.

pkg is never modified. Fall-through is pkg's own native mirror behaviour; the
facade's only lever is the status code.

## Request surface

### The worked example

A conventional pkg mirror URL looks like:

```
https://pkg.ghostbsd.org/stable/FreeBSD:15:amd64/latest/All/gopls-0.22.0_1.pkg
└──────── mirror root ───────┘└─── repo path ───┘└────┘└──── file ─────┘
```

With the daemon configured as a mirror, pkg's configured URL points at the
daemon (e.g. `http://127.0.0.1:9001/stable/FreeBSD:15:amd64/latest`) and the
daemon therefore receives the **path** portion:

```
/stable/FreeBSD:15:amd64/latest/All/gopls-0.22.0_1.pkg
```

### Package-file rule

A request is a **package-file request** if and only if, after path cleaning:

1. the second-to-last path segment is exactly `All`, **and**
2. the last segment ends in `.pkg`, **and**
3. stripping `.pkg` leaves a valid `name-version` string — a final hyphen
   separating a non-empty name from a version that starts with a digit
   (the same rule the cache watcher applies to cache filenames).

The package identifier is the last segment with `.pkg` removed —
`gopls-0.22.0_1` above. **Everything before `All/` is ignored**: the repo path
varies per mirror, per ABI and per branch, and carries no information the
daemon needs. The daemon matches on the tail, not the prefix.

Anything that fails the rule is a **non-package request** (UC-07): `meta.conf`,
`packagesite.pkg`, `data.pkg`, directory listings, `/`, and anything else. Note
that `packagesite.pkg` ends in `.pkg` but does not sit under `All/`, which is
precisely why condition 1 is load-bearing.

The signed catalog is the root of the integrity model and must always come from
a real mirror. The daemon never serves, caches or proxies metadata.

### Methods

`GET` only. See the open questions — `HEAD` is unresolved.

## Status codes

The use cases say only "an HTTP error" for every failure. These codes are an
implementer's choice. The governing convention:

> **4xx = this mirror legitimately does not have it. 5xx = this mirror is
> broken right now.**

Every non-200 makes pkg fall through to the next mirror, so the split is for
operators reading logs, not for pkg. Nothing in the daemon's behaviour depends
on which one is sent.

| Condition | Code | UC |
|---|---|---|
| Bytes fetched from a peer and hash-verified | `200` | UC-02 §10 |
| Non-package-file path (metadata, catalog, anything not `All/*.pkg`) | `404` | UC-07 §3 |
| Path is under `All/` and ends `.pkg` but the stem is not a valid name-version | `400` | — |
| Package is not in pkg's repository database (no expected hash) | `404` | — |
| Tracker returned an empty peer list | `404` | UC-02 §7b |
| Tracker unreachable, timed out, or sent an unparseable reply | `502` | UC-02 §6a |
| All peers tried; none produced verifying bytes (unreachable, errored, or hash mismatch) | `502` | UC-02 §9d |
| Every peer on the list is already blacklisted, so none is tried | `502` | UC-02 §7, §9d |
| Method other than `GET` | `405` | — |

Rationale for the two debatable ones:

- **Empty peer list → `404`, not `502`.** The tracker answered correctly; this
  mirror simply holds nothing. `GET /peers` returning `{"peers": []}` is an
  explicitly valid answer in the tracker spec, not an error, and the facade
  mirrors that.
- **All peers exhausted → `502`, not `404`.** Peers claimed to hold it and
  failed to deliver. That is an upstream fault, and it is the signal worth
  alerting on — a `404` would hide a swarm that is silently serving corrupt
  bytes.

`200` responses carry `Content-Type: application/octet-stream` and an accurate
`Content-Length`. Error responses carry a short `text/plain` body; pkg ignores
it, but a human running `curl` against the daemon should not get a blank page.

## Peer blacklist

The facade holds the daemon's local blacklist (UC-02 §7, §11c) for its whole
run, not per request: a peer whose bytes fail hash verification is marked
untrusted and skipped by every later fetch. Only a hash mismatch marks a peer —
an unreachable peer or a timeout costs one attempt and nothing more. The list
is local, in memory, never persisted, never reported to the tracker, and has no
expiry (nothing specifies one). See `docs/logs/claude-peer-blacklist.md`.

## What the facade does not do

- **No caching.** The daemon has no store. Every request is a fresh fetch.
- **No retry after a returned error.** One request, one verdict; pkg's
  fall-through is the retry mechanism.
- **No metadata proxying**, per UC-07 and the integrity model.

## Open questions — not resolved here

1. **`HEAD` requests.** Currently `405`. The facade cannot answer a `HEAD`
   honestly without performing the whole fetch (it does not know the size until
   it has the bytes), and answering dishonestly risks pkg believing a size it
   will not receive. Whether pkg issues `HEAD` against mirrors at all must be
   settled by the UC-07 integration smoke test.
2. **Hash format.** The facade asks the repository database for a hex SHA-256
   string, matching the assumption already isolated in `internal/peer/fetch.go`.
   Unratified.
3. **Repository database access.** No reader exists. The facade depends on the
   `PackageHashes` interface and is wired with a `nil` implementation until one
   lands, in which case every package-file request answers `404`.
4. **Temp buffer.** UC-02 §8 calls for streaming into the configured buffer
   directory. The current fetch path buffers in memory, so `BufferDir` is
   unused by the facade. Fixing this is a change to `internal/peer`, not here.
5. **Range requests.** pkg may issue them for resumed downloads. Unhandled;
   the facade ignores `Range` and returns the whole file with `200`.
