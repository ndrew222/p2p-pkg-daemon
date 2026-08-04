# Mirror Facade Spec — v0.1

*The pkg↔daemon HTTP surface (UC-02, UC-07). This is one of three separate
wires: `tracker-protocol-spec-v0.2.md` governs daemon↔tracker and
`peer-transfer-spec-v0.2.md` governs daemon↔daemon. Neither says anything about
this surface, and the path rule below is **not** shared with either — the peer
wire uses its own `/pkg/<name-version>` namespace precisely so that a seeding
daemon is not a syntactically valid pkg mirror.*

**Status: drafted by an implementing agent at the spec owner's instruction, not
by the spec owner.** The status codes were chosen by the implementer because
the use cases specify only "an HTTP error", and remain open to revision — treat
those as a record of what the code does, not as a ratified contract.

The **path rule is ratified.** It began as a generalisation from one worked
example; the `Hashed/` level and the `~hash10` suffix were then measured
against pkg 2.7.5 and the corrected rule was accepted by the owner.

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

1. some path segment is exactly `All`, and what follows it is either the file
   itself or the single segment `Hashed` and then the file, **and**
2. the last segment ends in `.pkg`, **and**
3. stripping `.pkg`, and then a trailing `~[0-9a-f]{10}`, leaves a valid
   `name-version` string — a final hyphen separating a non-empty name from a
   version that starts with a digit (the same rule the cache watcher applies to
   cache filenames).

Where more than one segment is `All`, the **last** one wins, so a repository
that happens to be named `All` cannot displace the real one.

The package identifier is the last segment with `.pkg` and any `~hash10`
removed — `gopls-0.22.0_1` above. **Everything before `All/` is ignored**: the
repo path varies per mirror, per ABI and per branch, and carries no information
the daemon needs. The daemon matches on the tail, not the prefix.

Anything that fails the rule is a **non-package request** (UC-07): `meta.conf`,
`packagesite.pkg`, `data.pkg`, directory listings, `/`, and anything else. Note
that `packagesite.pkg` ends in `.pkg` but does not sit under `All/`, which is
precisely why condition 1 is load-bearing.

### Why `Hashed/` and `~hash10`

Both were measured against FreeBSD 15.1-RELEASE-p1 / pkg 2.7.5, not inferred.
`pkg -d fetch -y -o /tmp/jmjprobe indexinfo` requests:

```
/…/All/Hashed/indexinfo-0.3.1_1~ae9dce33aa.pkg
```

and the repository database's `path` column agrees:
`All/Hashed/<name>-<version>~<hash10>.pkg`, where `hash10` is the first 10
characters of `cksum`.

An earlier revision of this rule required `All` to be the *second-to-last*
segment. Every real fetch from pkg 2.7.5 therefore failed condition 1, was
classified as metadata, and answered `404` — the daemon was a no-op against a
live repository. The rule above is the ratified fix.

The suffix match is deliberately narrow — exactly ten lowercase hex digits
after a tilde. A tilde is legal in a pkg version, so a looser rule would eat
part of a real version string and produce an identifier no peer holds.

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

## What the facade does not do

- **No caching.** The daemon has no store. Every request is a fresh fetch.
- **No retry after a returned error.** One request, one verdict; pkg's
  fall-through is the retry mechanism.
- **No metadata proxying**, per UC-07 and the integrity model.

## Open questions — not resolved here

1. **`HEAD` requests.** Currently `405`. The stated objection was that the
   facade cannot answer a `HEAD` honestly without performing the whole fetch,
   because it does not know the size until it has the bytes. That premise no
   longer holds: `packages.pkgsize` gives the exact size from the same
   repository-database row as the hash, and `peer-transfer-spec-v0.2.md` relies
   on it. The facade could therefore answer `HEAD` truthfully without fetching
   anything. Still **open**, because the remaining question — whether pkg
   issues `HEAD` against mirrors at all, and what it does with the answer — is
   for the UC-07 integration smoke test, not for this spec.
2. **Hash format.** ~~The facade asks the repository database for a hex SHA-256
   string, matching the assumption already isolated in
   `internal/peer/fetch.go`. Unratified.~~ **Resolved empirically:**
   `packages.cksum` is the lowercase hex SHA-256 of the `.pkg` file. All 37,835
   rows on the inspected host are 64 lowercase hex, verified byte-for-byte
   against three cached files. Residual risk: one repository, one ABI —
   `pkg_format_version` and `manifestdigest` exist in the schema and have not
   been investigated.
3. **Repository database access.** No reader exists. The facade depends on the
   `PackageHashes` interface and is wired with a `nil` implementation until one
   lands, in which case every package-file request answers `404`.
4. **Temp buffer.** ~~UC-02 §8 calls for streaming into the configured buffer
   directory. The current fetch path buffers in memory, so `BufferDir` is
   unused by the facade.~~ **Resolved** by `peer-transfer-spec-v0.2.md`: the
   fetch path streams into `os.CreateTemp` under a configurable temp directory
   and hashes incrementally, and `peer.FetchFromPeer` returns the open file
   rather than a byte slice. The facade copies that file to pkg and removes it.
   A `[]byte` return would have silently reintroduced whole-package residency
   at this layer, which is why the signature change matters here and not only
   in `internal/peer`.

   The facade now spools through `config.TempDir` and removes the file after
   serving, so the temp directory is wired and the cleanup path is tested. The
   memory saving is **not** yet realised: `peer.FetchFromPeer` still returns a
   `[]byte`, so the whole package is resident before the spool begins. That
   half arrives with the peer wire migration.
5. **Range requests.** pkg may issue them for resumed downloads. Unhandled;
   the facade ignores `Range` and returns the whole file with `200`.
