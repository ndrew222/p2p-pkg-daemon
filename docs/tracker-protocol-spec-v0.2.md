# Tracker Protocol Spec — v0.2

*Read this before writing tracker code. It is the contract; if something here is wrong or unclear, raise it — don't code around it.*

## Changes since v0.1

v0.1 left one item marked **"must be decided at kickoff"**: the wire encoding. It is now decided.

- **Wire encoding is HTTP + JSON.** Every message is an ordinary HTTP request; every reply is an HTTP status code plus (where noted) a small JSON body. This ratifies the shape the demo tracker already used (`GET /peers?…`, 404 on an unknown pinger) — see [§Wire encoding](#wire-encoding).
- **TIMEOUT and ping cadence are pinned:** `TIMEOUT = 60s`, `PING_INTERVAL = 20s`. Still constants/config, but with concrete defaults; the only hard rule is `PING_INTERVAL < TIMEOUT`.
- **Authentication: confirmed none for v0.x.** Recorded as a settled decision rather than an open question.
- **Storage: confirmed in-memory.** An implementation detail, not a protocol commitment. Nothing on the wire may depend on it.

Everything else in v0.1 — the state model, the message semantics, the robustness requirements — is unchanged. This revision is the encoding, not new behaviour.

One consequence to record, not to solve here: choosing one-shot HTTP forecloses the persistent daemon↔tracker connection that v0.1 flagged as the future signaling path for TCP simultaneous open. That is an accepted cost because NAT traversal is punted for MVP (ADR-001). If the simultaneous-open spike ever happens, it must supply its own out-of-band way for the tracker to reach a serving daemon (long-poll, SSE, a second channel — its problem, not this spec's).

## What the tracker is

A lookup service. Daemons tell it what packages they can serve; it answers "who has package X?" with addresses. That is all.

You do not need to know anything about package managers to build it. A "package" here is an opaque string of the form `name-version` (e.g. `nginx-1.24.0_2`) plus, on the daemon side, a file — but the tracker never sees the file. It matches strings and hands out addresses.

## What the tracker is not

- **Not a relay.** Package bytes never pass through it. It introduces peers; they talk directly.
- **Not a verifier.** It does not check that a daemon really has what it announces. Integrity is handled end-to-end: the downloading daemon verifies every file against a trusted hash. A lying announce costs one wasted transfer, nothing more.
- **Not persistent.** Its entire state can be an in-memory map. If it restarts and forgets everything, daemons re-register on their next ping. Losing state is safe by design.

## State

One entry per daemon, keyed by **public IP** (taken from the connection's source address — never from the message body):

```
IP  →  { servingPort, packageList, deadline }
```

- `servingPort`: the port the daemon listens on for peer transfers. Must come from the announce body — the tracker cannot infer it, because the source port of the daemon's *outbound* HTTP connection is unrelated to its listening port.
- `packageList`: the name-version strings this daemon can serve.
- `deadline`: now + `TIMEOUT`, refreshed on every ping or announce. When it passes, delete the entry. No notification is sent; expiry is silent.

Known limitation, accepted for now: one daemon per public IP. Two daemons behind the same NAT overwrite each other's entry.

## Wire encoding

**Transport:** HTTP/1.1 over TCP. One request, one response, connection may close after each — no persistent connection is assumed or required.

**Bodies:** JSON, `Content-Type: application/json`. The tracker reads request bodies where noted and always replies with a JSON body except on a bare `ack`, where an empty 200 is sufficient (a JSON body on an ack is permitted but not required).

**Framing:** handled by HTTP. No length-prefixing, no custom delimiters. `Content-Length` governs body size and is the first thing to bound (see [robustness](#robustness--non-negotiable)).

**Identity:** the daemon's IP is always the connection's source address. It is never read from a header or body. `X-Forwarded-For` and friends are ignored; the tracker is not run behind a trusted proxy in v0.x.

### Endpoint summary

| Logical message | HTTP request | Success reply | "Unknown / empty" reply |
|---|---|---|---|
| `ping()` | `POST /ping` (empty body) | `200` — deadline refreshed | `404` — tracker does not know this IP (**this 404 *is* `requestPackageList`**) |
| `announce(port, list)` | `POST /announce` + JSON body | `200` — registered or refreshed | `200` — empty list acked, entry deleted if it existed |
| `IWant(name-version)` | `GET /peers?pkg=<name-version>` | `200` + peer array (0–3 entries) | `200` + empty array (no match is not an error) |

Three paths, two verbs. `POST` for the two state-changing messages, `GET` for the read. That's the whole surface.

### `POST /ping`

Bare keep-alive. No body (any body is ignored).

- **Known IP:** refresh `deadline`. Reply `200`. Body optional; `{"status":"ack"}` is fine.
- **Unknown IP:** reply `404` with body `{"status":"unknown"}`. This is how the tracker says `requestPackageList`. The daemon's correct response is to send a `POST /announce` — it does **not** retry the ping. A `404` here is a normal control signal, not an error to log-and-panic over.

The 404-means-"announce-yourself" convention is exactly the tracker-restart self-healing path: tracker forgets everything → daemon's next ping gets `404` → daemon announces → re-registered. Do not "fix" the 404 into a 200; it is load-bearing.

### `POST /announce`

Body:

```json
{ "servingPort": 4711, "packages": ["nginx-1.24.0_2", "curl-8.6.0"] }
```

- `servingPort` — integer, 1–65535. Required. This is the daemon's peer-transfer listen port, unrelated to the source port of this HTTP request.
- `packages` — array of name-version strings. May be empty. This is a **full replacement**, never a delta.

Behaviour:

- **Non-empty list:** store/overwrite the entry `IP → {servingPort, packages, now+TIMEOUT}`. Reply `200`.
- **Empty list:** reply `200`, store nothing; if an entry exists for this IP, delete it. (An empty announce is how a daemon that just ran `pkg clean` deregisters, and how a fresh daemon confirms the tracker is reachable.)
- **Accepted from any IP,** known or unknown, solicited (after a 404 ping) or unprompted (a running daemon that just acquired a package announces its new full list directly).

Error replies:

- Malformed JSON, missing `servingPort`, wrong types, port out of range → `400`, entry untouched.
- `packages` array longer than `MAX_ANNOUNCE_LEN`, or body larger than `MAX_BODY_BYTES` → `413`, entry untouched. (See robustness — an attacker must not exhaust memory with one message.)

### `GET /peers?pkg=<name-version>`

Query parameter `pkg` carries one exact name-version string. Exact match only — no prefix, no fuzzy matching.

Reply `200` with:

```json
{ "peers": [ { "ip": "203.0.113.7", "port": 4711 }, { "ip": "198.51.100.4", "port": 5522 } ] }
```

- Up to `MAX_PEERS` entries (**`MAX_PEERS = 3`**, provisional — the 3-vs-1 privacy question from v0.1 is still unresolved; keep it a constant).
- No match → `200` with `{"peers": []}`. An empty result is a valid answer, not a `404`. (The only `404` in this protocol is the unknown-pinger signal on `/ping`.)
- Missing or empty `pkg` parameter → `400`.

## Messages — semantics (unchanged from v0.1, now with encoding)

| # | Logical message | Tracker behaviour |
|---|---|---|
| 1 | `ping()` → `POST /ping` | **Known IP:** refresh `deadline`, reply `200`. **Unknown IP:** reply `404` — the daemon follows up with an announce (message 3). |
| 2 | *(reply)* `requestPackageList` | Not a distinct message. It is the `404` reply to an unknown pinger. Listed so the exchange is visible. |
| 3 | `announce(port, list)` → `POST /announce` | Accepted from **any** IP, solicited or not. List is a **full replacement**, never a delta. **Non-empty:** store/overwrite, refresh `deadline`, reply `200`. **Empty:** reply `200`, store nothing, delete any existing entry. |
| 4 | `IWant(name-version)` → `GET /peers` | Reply `200` with up to `MAX_PEERS` entries `{ip, port}` whose lists contain that exact string, or `{"peers": []}`. Exact match only. |
| 5 | *(timer)* deadline expiry | Delete the entry. Silent. No reply — there is nobody to reply to. |

Every daemon *request* gets an HTTP response; there are no fire-and-forget messages. Expiry (5) is the sole tracker-internal event with no wire traffic.

### One complete life cycle

```
daemon boots        → POST /ping        → tracker: unknown IP → 404 {"status":"unknown"}
daemon              → POST /announce {servingPort:4711, packages:[nginx-1.24.0_2, curl-8.6.0]}
                                        → tracker: register, start clock → 200
every 20s           → POST /ping        → tracker: refresh clock → 200
daemon gets new pkg → POST /announce {servingPort:4711, packages:[nginx…, curl…, jq-1.7]}
                                        → tracker: overwrite, refresh clock → 200   (unprompted, full list)
someone runs pkg clean → POST /announce {servingPort:4711, packages:[]}
                                        → tracker: delete entry → 200
daemon now silent   — it must NOT keep pinging while it has nothing registered;
                      a ping would just get a 404 and loop for nothing.
                      The next non-empty announce re-registers it.
```

## Daemon-side obligations (context for the tracker team — implemented daemon-side)

- Ping cadence strictly less than `TIMEOUT` (`20s < 60s`).
- Treat `404` on `/ping` as the cue to announce, not as an error. Do not retry the ping; send `POST /announce`.
- Before announcing, sanity-filter the list (filename pattern and size checks — **no hashing**; hashing the whole cache on every announce is wasted disk I/O since the downloader verifies anyway).
- Suppress keep-alive pings while nothing is registered (see life cycle above).

## Robustness — non-negotiable

The tracker parses input from untrusted machines on the network. It is a declared fuzz target. Using `net/http` gets request-line and header parsing for free, but **every body and query parameter is still untrusted input.**

1. **Malformed input must never crash it.** Garbage bytes, truncated JSON, absurd values, wrong types, missing fields → reply `400`, log, discard, keep serving. A panic in a handler must not take the process down (recover per-request).
2. **Cap the body and the list.** Enforce `MAX_BODY_BYTES` (reject oversized bodies before reading them fully — `http.MaxBytesReader`) and `MAX_ANNOUNCE_LEN` (reject oversized `packages` arrays) → `413`. One message must not exhaust memory.
3. **Rate-limit per IP** (recommended): announces arrive in bursts (installing one package can pull dozens of dependencies, each triggering an announce); cheap per-IP throttling or debouncing keeps that survivable.

## Constants

| Name | Value | Notes |
|---|---|---|
| `TIMEOUT` | `60s` | Entry deadline. Config-overridable. |
| `PING_INTERVAL` | `20s` | Daemon-side cadence. Only hard rule: `< TIMEOUT`. |
| `MAX_PEERS` | `3` | `IWant` cap. Provisional pending the 3-vs-1 privacy question. |
| `MAX_ANNOUNCE_LEN` | *impl-chosen* | Cap on `packages` length. Pick a sane bound (e.g. a few thousand); document it. |
| `MAX_BODY_BYTES` | *impl-chosen* | Cap on request body size. Pick a sane bound; document it. |

All are constants/config, not wire-visible. A daemon never sends or reads them; changing them is not a protocol change.

## Settled decisions (was "deliberately unspecified" in v0.1)

| Item | Status |
|---|---|
| Wire encoding & framing | **Decided: HTTP + JSON.** See [§Wire encoding](#wire-encoding). Foreclosed: persistent-connection signaling for simultaneous open — accepted, NAT traversal is punted (ADR-001). |
| TIMEOUT value, ping cadence | **Pinned:** `60s` / `20s`. Config-overridable; `cadence < timeout` is the only invariant. |
| Storage | In-memory map. Implementation detail; nothing on the wire depends on it. A database is a private choice, not a protocol change. |
| Authentication | **None in v0.x.** Announces are trusted; consequences are availability-only and self-correcting. Revisit only if a threat model demands it. |

Nothing in this table is open at kickoff. There is nothing left to decide before code.

## Definition of done

A Go binary + package with tests demonstrating, over the HTTP surface above:

- The full life cycle: unknown-ping `404` → announce `200` → keep-alive pings refresh → updated announce overwrites → empty announce deregisters.
- Expiry flushes entries after `TIMEOUT` with no ping.
- Empty announce deregisters (and acks `200` even with nothing to delete).
- `GET /peers` returns correct matches, caps at `MAX_PEERS`, returns `{"peers": []}` cleanly on a miss.
- `POST /ping` from a known IP is `200`; from an unknown IP is `404`.
- Malformed input of every message type is survived: bad JSON, wrong types, missing `servingPort`, out-of-range port, oversized body (`413`), oversized list (`413`), missing `pkg` param (`400`) — process stays up throughout.

All tests run on any OS — no FreeBSD, no package manager, no second machine required. The malformed-input cases double as the seed corpus for the fuzz target.
