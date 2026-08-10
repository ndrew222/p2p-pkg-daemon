This is the command for the tracker

`trac` is the swarm's directory: one per swarm, and the only thing every daemon
talks to. It answers one question — *who claims to hold this package?* — and
deliberately answers nothing else.

## Running it

```sh
trac                    # :8080
trac -addr :9999        # anywhere else
trac -addr 127.0.0.1:8080
```

One flag, no config file, no state on disk. The registry is an in-memory map
that starts empty and is rebuilt by the peers themselves: a tracker that
restarts forgets everyone, the next ping from each daemon earns a `404`, and
every daemon answers a `404` by announcing. That is the entire recovery
mechanism and it needs no persistence.

**It runs on any operating system.** No FreeBSD, no `pkg`, no cgo — the tracker
and its tests are required to build and pass anywhere (`AGENTS.md`), so putting
it on a Linux box or a laptop is normal rather than a compromise.

Placement is a separate question from portability: **every daemon must be able
to reach it**, so it wants a public address. A tracker behind NAT needs a
forwarded port. See `docs/logs/claude-demo-guide.md` §3.1.

## The surface

Three paths, specified by `docs/tracker-protocol-spec-v0.2.md` (encoding) and
`docs/protocol-spec-v0.1.md` (semantics).

| Request | Body | Meaning |
|---|---|---|
| `POST /announce` | `{"servingPort":9002,"packages":["nginx-1.24.0_2",…]}` | This is my **complete** list. Never a delta. |
| `POST /ping` | none (any body ignored) | Still here. `200` if known, **`404` if not**. |
| `GET /peers?pkg=<name-version>` | — | Up to `MaxPeers` (3) holders, as `{"ip","port"}`. A miss is `200` with `[]`. |

Three things about that table are load-bearing:

- **A daemon's IP is the connection's source address**, always. It is never read
  from the body or from a header — `X-Forwarded-For` and friends are ignored,
  and the tracker is not run behind a trusted proxy. Every entry is keyed on
  this value, so a header fallback would let any client claim any address.
  One consequence to plan around: **one daemon per public IP.** Two daemons
  behind the same NAT overwrite each other's entry.
- **The `404` from `/ping` is a message, not an error.** It is the protocol's
  *requestPackageList*: the daemon's correct response is to announce, not to
  retry the ping. Do not "fix" it into a `200`.
- **An empty announce deregisters.** It is the clean-shutdown path and reaches
  the registry so the entry is deleted rather than left to expire.

Entries live `Timeout` (60s) without a ping or announce; daemons ping every
`PingInterval` (20s), and the only rule the spec fixes is that the interval is
shorter than the timeout. A sweeper reaps expired entries every 15s, but that is
housekeeping only — `/peers` already ignores anything past its deadline, so an
entry cannot be handed out late even between sweeps.

## What it never does

- **It never relays package bytes.** Peers fetch from each other directly; the
  tracker's replies are addresses.
- **It never verifies content.** No hashes, no sizes, no fetching to check.
  Integrity comes from pkg's own repository database, on the requesting daemon,
  after the bytes arrive.
- **It never learns that a peer is bad.** Blacklisting is local to each daemon
  and is never reported here.

So an announce is a *claim*, not a fact, and the design assumes some claims are
wrong. A daemon that announces a package it cannot serve costs the requester one
failed attempt and nothing else. There is no authentication in v0.x for the same
reason: the consequences are availability-only and self-correcting.

## Seeing it work

`docs/logs/claude-demo-guide.md` §1.2 drives all three endpoints with `curl` in
about a minute — including the three-peer cap, deregistration and the `404` —
using several loopback source addresses to stand in for several peers.
