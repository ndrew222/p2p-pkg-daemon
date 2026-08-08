# Mirror Facade Spec — v0.1 — **DEPRECATED**

> # ⛔ DEPRECATED — do not implement from this document.
>
> **Deprecated by the owner, 2026-08-08. `docs/adr/adr-003-facade-fetch-semantics.md` is its successor.** There is no v0.2 and none is planned; the facade is governed by ADRs from here.
>
> It was never binding in the first place — its own status block below records that it was drafted by an implementing agent rather than the spec owner, and that the status codes "remain open to revision". ADR-003 then overruled the fall-through model the whole document was built on.
>
> **Where its content went:**
>
> | Section | Now governed by |
> |---|---|
> | Fetch semantics, status codes, verification placement, no-cache rule | `docs/adr/adr-003-facade-fetch-semantics.md` |
> | *Request surface* — the `All/` + `Hashed/` + `~hash10` path rule, and `GET`-only | `docs/adr/adr-004-facade-path-rule.md` **(Proposed — needs vetting)** |
> | Peer blacklist | UC-02 §7/§11c and `docs/logs/claude-peer-blacklist.md` |
> | Open questions 6 and 7 (upstream mirror config; metadata proxying) | `docs/logs/HANDOFF.md` §4.5 and §4.4 — both still open |
>
> **Until ADR-004 is approved, the path rule in *Request surface* below is still the only specification of a rule that shipped code depends on** (`internal/daemon/facade.go`, `watcher.go`, `repodb.go`). That is the one part of this document not yet safe to ignore. Everything else is history: kept because it records why the design changed, not because it should be obeyed.
>
> Retained rather than deleted for the same reason the struck-through text inside it is retained — a reader who finds only the conclusion has nothing to stop them re-proposing the idea that was measured wrong.

---


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

> **Partly superseded by `docs/adr/adr-003-facade-fetch-semantics.md` (approved).**
> This spec was written on the assumption that a facade error makes pkg fall
> through to its next configured mirror. That assumption was measured and is
> **false**: pkg falls through between *mirrors within one repository*, never
> between *repositories*, and jmj is configured as a repository. A facade error
> ends the install. ADR-003 replaces the fall-through model with **proxy to an
> upstream mirror on a peer miss**, and outranks this document wherever the two
> disagree. Evidence: `docs/logs/claude-pkg-mirror-verification.md` §7.1.
>
> Revised below: *What the facade is*, *Status codes*, *What the facade does
> not do*, and open questions 1, 4 and 5. **Unaffected:** the whole *Request
> surface* section — the `All/` + `Hashed/` + `~hash10` path rule is measured,
> ratified and independent of what the facade does after it has parsed a path.

The **path rule is ratified.** It began as a generalisation from one worked
example; the `Hashed/` level and the `~hash10` suffix were then measured
against pkg 2.7.5 and the corrected rule was accepted by the owner.

## What the facade is

The daemon is configured as pkg's **only mirror** (ADR-003). pkg — never the
user — makes ordinary HTTP mirror requests to it. The facade tries to answer a
package-file request from a peer; when it cannot, it **fetches the package from
a configured upstream mirror and serves those bytes**. There is no next mirror
to fall through to, so an error is a failed install rather than a redirection.

pkg is never modified. What changed is that the facade's lever is no longer the
status code — it is where the bytes come from.

**Verification placement is asymmetric** (ADR-003):

- **Peer path — spool and verify before serving.** Stream into `temp_dir`,
  bounded by the expected size, hash incrementally, serve only on a match. The
  buffer buys the ability to abandon a bad peer and try another without pkg
  ever seeing a failure; with `MAX_PEERS = 3` that is worth having, and the
  blacklist needs the verdict anyway.
- **Upstream path — stream straight through.** No spool. Upstream is the
  terminal source: if its bytes are bad there is no next source to try, so
  withholding them buys nothing. pkg re-verifies every byte it is handed
  against the same signed catalogue (UC-02 §10), which is where integrity
  actually comes from. A mid-transfer upstream failure yields a short body
  against a promised `Content-Length` — exactly what pkg sees from a real
  mirror having a bad day, and handles routinely.

The exact `Content-Length` for the upstream path comes from `packages.pkgsize`
in the same repository-database row as the hash, so the facade commits to a
correct length rather than guessing.

**Streaming must not reintroduce a `[]byte`.** The largest package is 2.83 GiB
and the reference host has 1 GiB; `AGENTS.md` forbids whole-package residency
on the peer wire and the same discipline binds the upstream path, which must be
written that way from the start rather than retrofitted.

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

> ⚠️ **OPEN — do not implement either reading. Ask the owner.** The sentence
> above is in direct tension with ADR-003 and the ADR does not settle it. Its
> *Decision* section rules only on package files, but it also makes jmj pkg's
> **only** mirror — and §7.1 measured that a facade which fails a metadata
> request breaks `pkg update` outright, because there is no second repository
> to fall through to. The §7 harness in fact proxied the signed catalogue from
> a real mirror, and ADR-003 cites that run approvingly and expects the facade
> to relay upstream's `304` to a conditional `GET`, which is metadata
> proxying. So the model ADR-003 was drafted from appears to require it.
>
> Note that relaying is not the same as *vouching*: the bytes still originate
> at the real mirror and pkg still verifies the repository signature itself, so
> "the catalog comes from a real mirror" can survive a pass-through. But
> "never proxies metadata" cannot. **Which of the two sentences gives way is an
> owner ruling under ground rule 2, and it needs its own ADR** — it also
> decides UC-07, which is built end to end on the fall-through that does not
> exist.

### Methods

`GET` only. Measured: pkg 2.7.5 issues nothing else against a mirror — see
open questions 1 and 5.

## Status codes

**Revised under ADR-003.** The old table treated every non-`200` as a
fall-through signal — "pkg will just ask the next mirror" — and chose between
`404` and `502` purely for operators reading logs. That premise is gone. A
non-`200` now **fails the install**, so the interesting question is no longer
which code to send but *when the facade is entitled to send one at all*. The
answer is: only when it can prove it has nothing to serve.

The governing rule is now:

> **Every peer-side failure falls through to upstream, not to pkg.** An error
> reaches pkg only when the peers *and* upstream have both failed, or when the
> request is one no mirror could satisfy.

| Condition | Code | UC |
|---|---|---|
| Bytes fetched from a peer and hash-verified | `200` | UC-02 §10 |
| Bytes streamed from the upstream mirror after a peer miss | `200` | ADR-003 |
| Path is under `All/` and ends `.pkg` but the stem is not a valid name-version | `400` | — |
| Package is not in pkg's repository database (no expected hash) | `404` | — |
| Method other than `GET` | `405` | — |
| Peers unavailable *and* the upstream fetch also failed | `502` | ADR-003 |
| Non-package-file path (metadata, catalog, anything not `All/*.pkg`) | ⚠️ **open** | UC-07 — see the warning above |

"Peers unavailable" collapses four conditions the old table listed separately —
tracker unreachable, tracker returned an empty list, every holder blacklisted,
and all holders tried without verifying bytes. Under ADR-003 they no longer
need distinguishing in the response, because **all four now go to upstream**
and none of them is visible to pkg on its own. They remain worth
distinguishing in the *log*, and diagnostics should keep naming which one
occurred.

Rationale for what is left:

- **`404` narrows to "provably absent."** It stops being the fall-through
  signal and survives only for a package the repository database does not
  contain — a request the facade can prove is unanswerable, where going to
  upstream would be pointless because there is no expected hash or size to
  bound the transfer with. UC-06 §5b's re-announce obligation attaches to the
  *peer* wire's `404`, not to this one, and is untouched.
- **`502` narrows to "both sources failed."** It is now the only case in which
  the facade has genuinely nothing to serve, which makes it the code worth
  alerting on. Previously it was one of four routine outcomes.
- **An empty peer list is no longer an error at all.** It is the common case —
  a package nobody nearby has yet — and it is exactly what the upstream path
  exists to absorb. Answering `404` to it, as the old table did, would fail
  every first-of-its-kind install.

`200` responses carry `Content-Type: application/octet-stream` and an accurate
`Content-Length` — on the upstream path taken from `packages.pkgsize`, not from
counting bytes as they arrive. Error responses carry a short `text/plain` body;
pkg ignores it, but a human running `curl` against the daemon should not get a
blank page.

## Peer blacklist

The facade holds the daemon's local blacklist (UC-02 §7, §11c) for its whole
run, not per request: a peer whose bytes fail hash verification is marked
untrusted and skipped by every later fetch. Only a hash mismatch marks a peer —
an unreachable peer or a timeout costs one attempt and nothing more. The list
is local, in memory, never persisted, never reported to the tracker, and has no
expiry (nothing specifies one). See `docs/logs/claude-peer-blacklist.md`.

## What the facade does not do

- **No caching — including of what it proxies.** The daemon has no store and
  ADR-003 does not create one. The upstream bytes pass through the facade, so
  it plainly *could* cache them, and it must not: `AGENTS.md` allows writes
  only to the temp buffer directory, UC-02 assumes "the daemon has no package
  store of its own", and UC-06 assumes "there is no daemon-owned store to
  poll". It would also be pointless — UC-02 §10 has pkg write the served bytes
  into `/var/cache/pkg`, which is the directory the daemon seeds from, so a
  proxied package joins the swarm anyway and a facade cache would be a second
  copy of the same bytes on the same disk. And it would reinstate precisely the
  I/O that streaming was chosen to avoid.
- **No retry after a returned error.** One request, one verdict. Note this no
  longer means what it used to: the retry mechanism is now the facade's own
  peer loop followed by the upstream fetch, not pkg's fall-through. By the time
  a code reaches pkg, every source has been tried.
- **No metadata proxying**, per UC-07 and the integrity model — ⚠️ **but see
  the open flag under *Request surface*.** ADR-003 appears to require the
  opposite and does not say so explicitly. Unresolved.

## Open questions — not resolved here

1. **`HEAD` requests.** ~~Still open pending the UC-07 smoke test.~~
   **Answered — leave it at `405`.** The measurement has been taken
   (`docs/logs/claude-pkg-mirror-verification.md` §7.3): across a catalogue
   refresh, a `pkg fetch` and a real `pkg install`, **pkg 2.7.5 issued zero
   `HEAD` requests** against the mirror. Every request was a plain `GET`;
   user-agents `pkg/2.7.5` and `fetch libfetch/2.0`. Since nothing asks, the
   question of whether the facade *could* answer honestly — it could, via
   `packages.pkgsize` — is moot, and `405` costs nothing. Scope the result
   honestly: it says pkg does not use `HEAD` on the paths exercised, not that
   it never will.
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

   **ADR-003 narrows why the spool exists, and confines it to one path.** The
   stated reason — "verification needs the whole file before any byte may reach
   pkg" — was never quite true, because integrity comes from pkg re-verifying
   (UC-02 §10), not from the buffer. What the buffer actually buys is the
   ability to abandon a bad source and try another *without pkg seeing a
   failure*, since a `200` is committed the moment its first body byte is
   written. That is worth having on the peer path, where `MAX_PEERS = 3`
   leaves somewhere to go, and worth nothing on the upstream path, where there
   is no next source. So `temp_dir` keeps its consumer and its honest
   justification is "**retry** needs the whole file"; the upstream path does
   not spool at all.
5. **Range requests.** ~~pkg may issue them for resumed downloads.~~
   **Measured: it does not** (§7.3) — zero `Range` headers across every
   observed transfer. Ignoring `Range` and returning the whole file with `200`
   is therefore correct for the normal path, and it stays.

   **The caveat is real and not yet closed:** every observed transfer was small
   and none was interrupted. Resume-after-interrupt was never exercised, and a
   `Range` request is exactly what would plausibly appear there. A facade that
   ignores `Range` and answers `200` to a resume attempt hands back the whole
   file where the client asked for a suffix — pkg would then have a body that
   does not match what it asked for. Worth an interrupted-download test before
   this is called settled.

6. **Which upstream mirror, and how it is configured.** ADR-003 requires a
   configured upstream but deliberately leaves this open: whether there is a new
   config key at all, TLS to that mirror, and the choice of mirror itself.
   **Owner decision — do not invent a key name.**

   ~~`pkg+https://pkg.FreeBSD.org/${ABI}/quarterly` resolves via DNS SRV, so the
   daemon must either resolve SRV itself or be pointed at a concrete host.~~
   **Wrong — measured 2026-08-08.** `pkg.FreeBSD.org` is a CNAME to
   `pkgmir.geo.FreeBSD.org` with ordinary A and AAAA records, so Go's stdlib
   HTTP client reaches it unaided; and `_https._tcp.pkg.FreeBSD.org` holds a
   single SRV target (`10 10 443 pkgmir.geo.freebsd.org.`) naming that same
   host on the standard port. SRV buys nothing plain DNS does not. Do not
   hand-roll a resolver. See `docs/logs/HANDOFF.md` §4.5 for the candidate of
   discovering the URL from `/etc/pkg/FreeBSD.conf` rather than adding a key.

7. **Metadata proxying.** See the flag under *Request surface*. ADR-003 makes
   jmj pkg's only mirror, which appears to force it, while this spec and UC-07
   forbid it. Needs its own ADR.
