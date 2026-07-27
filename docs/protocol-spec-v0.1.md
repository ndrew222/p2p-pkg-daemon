# Tracker Protocol Spec — v0.1
 
*Read this before writing tracker code. It is the contract; if something here is wrong or unclear, raise it — don't code around it.*
 
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
 
- `servingPort`: the port the daemon listens on for peer transfers. Must come from the announce message — the tracker cannot infer it, because the source port of the daemon's *outbound* connection is unrelated to its listening port.
- `packageList`: the name-version strings this daemon can serve.
- `deadline`: now + TIMEOUT, refreshed on every ping or announce. When it passes, delete the entry. No notification is sent; expiry is silent.
Known limitation, accepted for now: one daemon per public IP. Two daemons behind the same NAT overwrite each other's entry.
 
## Messages
 
Four message types from daemons, two replies of note, one timer.
 
| # | Message (daemon → tracker) | Tracker behavior |
|---|---|---|
| 1 | `ping()` | **Known IP:** refresh `deadline`, reply `ack`. **Unknown IP:** reply `requestPackageList` — the daemon will follow up with an announce (message 3). |
| 2 | *(reply)* `requestPackageList` | Not a daemon message — the tracker's reply to an unknown pinger. Listed here so the exchange is visible. |
| 3 | `announce(listeningPort, packageList)` | Accepted from **any** IP, known or unknown, solicited or not — daemons announce unprompted when they acquire a new package. The list is a **full replacement**, never a delta. **Non-empty list:** store/overwrite the entry, refresh `deadline`, reply `ack`. **Empty list:** reply `ack` but store nothing; if an entry exists, delete it. (An empty announce is how a daemon that just emptied its cache deregisters — and how a fresh daemon confirms the tracker is reachable.) |
| 4 | `IWant(name-version)` | Reply with up to **3** entries `{IP, servingPort}` whose lists contain that exact string, or an empty list. Exact match only — no prefix or fuzzy matching. (The cap of 3 is provisional; a privacy question about 3-vs-1 is unresolved. Make it a constant.) |
| 5 | *(timer)* deadline expiry | Delete the entry. Silent. |
 
Every daemon message gets a reply; there are no fire-and-forget messages.
 
### One complete life cycle
 
```
daemon boots        → ping           → tracker: unknown IP → requestPackageList
daemon               → announce(4711, [nginx-1.24.0_2, curl-8.6.0])
                                     → tracker: register, start clock → ack
every T seconds     → ping           → tracker: refresh clock → ack
daemon gets new pkg → announce(4711, [nginx…, curl…, jq-1.7])   (unprompted, full list)
                                     → tracker: overwrite, refresh clock → ack
someone runs pkg clean → announce(4711, [])
                                     → tracker: delete entry → ack
daemon now silent   — it must NOT keep pinging while it has nothing registered;
                      pinging would just loop the unknown-IP exchange for nothing.
                      The next non-empty announce re-registers it.
```
 
## Daemon-side obligations (context for the tracker team — implemented daemon-side)
 
- Ping cadence strictly less than TIMEOUT.
- Before announcing, the daemon sanity-filters its list (filename pattern and size checks — **no hashing**; hashing the whole cache on every announce is wasted disk I/O since the downloader verifies anyway).
- Suppress keep-alive pings while nothing is registered (see life cycle above).
## Robustness — non-negotiable
 
The tracker parses input from untrusted machines on the network. It is a declared fuzz target.
 
1. **Malformed input must never crash it.** Garbage bytes, truncated messages, absurd values → log, discard, keep serving.
2. **Cap the announce list size.** Reject oversized announces; an attacker must not be able to exhaust memory with one message.
3. **Rate-limit per IP** (recommended): announces can arrive in bursts (installing one package can pull in dozens of dependencies, each triggering an announce); cheap per-IP throttling or debouncing keeps that survivable.
## Deliberately unspecified — do not invent, ask
 
| Item | Status |
|---|---|
| TIMEOUT value, ping cadence | Symbolic. Make them constants/config; values decided later. |
| Storage | In-memory map is fine. A database is an internal choice; nothing in the protocol may depend on it. |
| Wire encoding & framing (JSON over HTTP? length-prefixed over raw TCP?) | **Must be decided at kickoff, before code.** One consideration to weigh: a persistent daemon↔tracker connection would later double as the signaling path for TCP simultaneous open (the tracker must be able to tell a *serving* daemon "dial out now"), which one-shot HTTP requests cannot do. |
| Authentication | None in v0.1. Announces are trusted; consequences are availability-only and self-correcting. |
 
## Definition of done
 
A Go binary + package with tests demonstrating: the full life cycle above; expiry flushes entries; empty announce deregisters; `IWant` returns correct matches, caps at 3, and returns empty cleanly; malformed input of every message type is survived. All tests run on any OS — no FreeBSD, no package manager, no second machine required.

