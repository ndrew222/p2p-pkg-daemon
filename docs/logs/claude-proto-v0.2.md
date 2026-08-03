# Work log — proto/tracker migration to v0.2

Author: claude
Scope: replace CID/`peer_id` addressing with name-version addressing across the
wire layer, per `docs/tracker-protocol-spec-v0.2.md`.

## Why this came first

The mirror facade (UC-02/UC-07) is finished and tested but cannot be connected
to anything real. `internal/peer.PeerLister` wants
`Peers(nameVersion string) ([]string, error)`; `internal/discovery.Client`
provides `Peers(cid string) ([]proto.PeerInfo, error)`. The signature mismatch
is the visible half. The blocking half is `proto.ValidateCID`, whose
`^[0-9a-f]{64}$` pattern rejects `nginx-1.24.0_2` on both the query path
(`Client.Peers`) and the announce path (`AnnounceRequest.Validate` runs it over
every entry). No adapter can paper over that; the wire layer had to change.

AGENTS.md already records CIDs, `peer_id` and IPFS as deprecated, so this is
not a spec decision — it is catching the code up to a decision already made.

## Commit 1 — `internal/proto`

### Removed

| Gone | Why |
|---|---|
| `PeerID` | v0.2 §State keys entries by public IP taken from the connection's source address. There is no peer identity on the wire. |
| `PingRequest` | v0.2 §POST /ping: "Bare keep-alive. No body (any body is ignored)." There is nothing to encode. |
| `AnnounceRequest.Addr` | The tracker learns the IP from the connection and the port from `servingPort`. §Wire encoding: identity "is never read from a header or body". |
| `ValidateCID`, `cidPat`, `CIDLen`, `MaxCIDs` | CID addressing is deprecated. |
| `validatePeerID`, `peerIDPat`, `MaxPeerIDLen` | No peer identity. |
| `validateAddr`, `MaxAddrLen` | No address field on the wire. Replaced by `PeerInfo.Validate`, which checks the IP and port the tracker hands back. |
| `PeerListResponse.CID` | The reply no longer echoes the query. §GET /peers shows `{"peers": [...]}` only. |

### Added / changed

- `AnnounceRequest{ServingPort int, Packages []string}` — the exact body from
  §POST /announce. **An empty `Packages` list validates**: it is the
  deregistration path (`pkg clean` → empty announce → tracker deletes the
  entry), so it has to pass validation and reach the tracker.
- `PeerInfo{IP string, Port int}` with `Addr()` returning `host:port` via
  `net.JoinHostPort`, so IPv6 literals come back bracketed. The facade's
  eventual `PeerLister` wants `[]string`, and this is where that string is
  built.
- `PeerListResponse.Validate()` — the tracker is not fully trusted (its reply
  feeds a dialler), so every peer's IP must parse and its port must be in
  range before the daemon acts on it. Matches the existing bounded-read
  posture in `discovery.readLimited`.
- `StatusResponse` + `StatusAck`/`StatusUnknown` — the `{"status":"unknown"}`
  body on the load-bearing 404.
- `MaxBodyBytes` exported (was unexported `maxBodySize` in codec.go) and
  `MaxPackages = 4096` named. §Constants leaves `MAX_BODY_BYTES` and
  `MAX_ANNOUNCE_LEN` implementation-chosen but requires them documented; the
  tracker also needs `MaxBodyBytes` for `http.MaxBytesReader`. 4096 carries
  over the value the old `MaxCIDs` had.

### The one judgement call: `ValidateNameVersion` is deliberately permissive

It checks non-empty, `<= 255` bytes, and no C0/DEL control characters. That is
all. `nginx` (no version) and `nginx-latest` (version not digit-initial) both
**pass** proto validation, and there is a test asserting they do.

This is not an oversight. v0.2 §"What the tracker is" calls a package "an
opaque string of the form `name-version`" and §GET /peers specifies "exact
match only — no prefix, no fuzzy matching". No document in `docs/` defines the
name-version grammar. `internal/peer/fetch.go` already flagged this exact gap
("the exact name-version format is not specified in the docs … do not invent a
stricter rule here"), and AGENTS.md says ambiguities are to be raised, not
resolved by picking a reasonable interpretation.

So the wire layer validates only what the layers below actually need: it is a
map key (non-empty), it is attacker-controlled (bounded), and it reaches log
lines (no control characters — the `%q` log-injection defence the existing
proto comments already argue for).

The structural rule — split on the last hyphen, version must start with a digit
— already exists in `internal/daemon.parsePackageName` and is where v0.2
§"Daemon-side obligations" puts it: a daemon-side sanity filter before
announcing. It is not duplicated in proto, because a *remote* peer is allowed
to announce a string this daemon would not itself have generated, and the
tracker is specified to match it verbatim.

### Tests

`validate_test.go` and `codec_test.go` are new (proto had no tests). The JSON
bodies in `codec_test.go` are copied verbatim from v0.2 so that struct-tag
drift fails a test rather than silently shipping. `TestDecodeMalformed` is the
seed corpus the spec's definition-of-done asks for. `TestDecodeRejectsV01Announce`
pins the useful side effect of `DisallowUnknownFields`: a v0.1 daemon talking
to a v0.2 tracker is rejected loudly instead of being registered with an empty
package list.

## Commit 2 — `internal/tracker`

State is now `IP -> {servingPort, packages, deadline}` (§State). The old
`peerID -> {addr, lastSeen, cids}` plus `cid -> holders` pair becomes
`ip -> record` plus `nameVersion -> set of IPs`. Same two-map shape, different
keys; the reverse index still exists so a lookup does not scan every
registration.

Behavioural changes that are not just renames:

- **`Ping` takes an IP, not a request.** There is no ping body in v0.2. It
  also now rejects a ping from an entry that is *past its deadline but not yet
  swept*: the old code compared `lastSeen` only in `Peers`, so a daemon that
  went dark for longer than the timeout could keep its stale package list alive
  by pinging in the gap before the sweeper ran.
- **Empty announce deletes the entry** rather than storing an empty list. This
  is the `pkg clean` deregistration path.
- **`Peers` caps at `MaxPeers = 3`** and returns a non-nil empty slice on a
  miss. Non-nil matters on the wire: a nil slice marshals to `null`, and the
  spec requires `{"peers": []}`. Map iteration order is random so which three
  holders a caller gets varies — left alone deliberately, it spreads load for
  free.
- **`Deadline` replaces `LastSeen`.** The spec models expiry as a deadline; a
  stored deadline also makes the per-tracker `timeout` override work without
  every comparison site having to know about it.
- **`NewWithTimeout`** exists because §Constants says `TIMEOUT` is
  config-overridable, and because the expiry tests would otherwise take a
  minute each.

`tracker` had no tests either. `TestLifeCycle` walks §"One complete life
cycle" in order. The announce helper validates every fixture through
`proto.AnnounceRequest.Validate` first, so a test cannot pass on a body the
wire layer would reject. `TestOneDaemonPerIP` pins the known NAT limitation the
spec accepts, so it can only change deliberately.

## Commit 3 — `cmd/trac`

The handlers were v0.1: they read a `peer_id` out of the body, answered `204`,
and queried on `?cid=`.

- **Success is `200`, not `204`.** The §Endpoint summary table says `200` for
  all three paths. `204` was never spec'd; it just happened to be what the
  demo returned.
- **Identity comes from `r.RemoteAddr`**, via `net.SplitHostPort`. There is a
  test (`TestIdentityIgnoresForwardedHeaders`) asserting `X-Forwarded-For` and
  `X-Real-IP` are ignored, because the moment a header is trusted, any client
  can register on any other machine's behalf and every entry in the registry
  is keyed on that value.
- **`POST /ping` parses nothing.** Any body is drained and discarded, not
  rejected. The 404 carries `{"status":"unknown"}` and has a comment on it
  saying it is load-bearing, because it looks exactly like a bug to anyone
  reading the handler cold.
- **`413` is now distinct from `400`.** `http.MaxBytesReader` (not
  `io.LimitReader` — the latter silently truncates an oversized body into
  something that might still parse) gives the oversized-body 413;
  `proto.ErrTooManyPackages` gives the oversized-list 413. Everything else
  malformed stays 400.
- **`recoverPanics` wraps the mux.** `net/http` already recovers per
  connection, but it kills the connection silently; this returns a 500 and
  logs the path.
- **`newMux` is split out of `main`** so the tests drive real routing,
  including the 405s that come from the method patterns.

`TestMalformedInput` runs every case from the §Definition of done list against
one tracker and then asserts it is still serving correctly, which is the
actual claim being made. It doubles as the fuzz seed corpus.

## Commit 4 — `internal/discovery`

`keepalive.go` was already written for v0.2 — its `Tracker` interface is
literally `Ping() error` / `Announce(port int, packages []string) error`, with
a comment saying "discovery.Client will satisfy this once wire spec v0.2
lands". This commit is that. The comment is now a compile-time assertion.

So `Client.RunHeartbeat` was **deleted, not fixed**. `KeepAlive` supersedes it
and is strictly better: `RunHeartbeat` pinged unconditionally, where `KeepAlive`
tracks `registered` and stays quiet while nothing is on the tracker, which is
the daemon-side obligation in §"One complete life cycle" ("it must NOT keep
pinging while it has nothing registered; a ping would just get a 404 and loop
for nothing"). `Stop`/`stopChan` went with it — `KeepAlive.Run` already takes a
`done` channel.

`Client` also lost its `peerID` and `addr` fields. There is no identity to
carry: the tracker keys on the connection's source IP.

The two assertions in `client.go` are the whole point of the migration:

```go
var (
	_ Tracker         = (*Client)(nil)
	_ peer.PeerLister = (*Client)(nil)
)
```

The second one is what was impossible before. `internal/peer` imports only
`peerwire`, so `discovery -> peer` introduces no cycle.

Other changes:

- `Peers` returns `[]string` of dialable `host:port`, built through
  `PeerInfo.Addr()`. It validates the reply before returning it — the tracker
  is not fully trusted and its response feeds a dialler.
- An empty peer list is **not** an error. The facade maps "no peers" to 404
  and "tracker unreachable" to 502, and pkg behaves differently for each, so
  collapsing them here would break UC-02.
- `Ping` sends no body and no Content-Type, via `http.NewRequest` rather than
  `Post`.
- Success is `200`, not `204`.

## Commit 5 — call sites, and the gate back to green

Mechanical, except where noted.

- `cmd/jmj` loses `-id` and renames `-cids` to `-packages`.
- `daemon.Daemon` loses `peerID`, renames `cids` to `packages`, and drives
  `discovery.KeepAlive` instead of the deleted `Client.RunHeartbeat`.
- `cmd/demo/main.go.new` deleted — it was untracked, CID-addressed, and
  imported a package path that does not exist. `cmd/demo/main.go` itself was
  already name-version based; only two stale comments needed fixing.

Three things in here are not mechanical:

1. **`Reload` deadlocked.** It took `d.mu` and then called `startHTTPServer`,
   which took `d.mu` again. `sync.Mutex` is not reentrant, so any SIGHUP that
   changed `listen_addr` hung the daemon permanently. Pre-existing, not caused
   by this migration, but it was in code this commit rewrites, so leaving it
   would have meant knowingly shipping a deadlock. The internal helpers are now
   `…Locked` and the exported entry points take the lock once.
2. **A tracker that is down at startup is no longer fatal.** `KeepAlive` owns
   the initial announce, so `Start()` cannot return its error. This is the
   right behaviour — v0.2's whole self-healing story is that a daemon survives
   the tracker going away — but it is a behaviour change, not a refactor.
3. **`staticCache` is a placeholder.** It reports the list from `-packages` and
   never changes, so the daemon does not notice `pkg install` or `pkg clean`.
   The real source is `daemon.Watcher`, which is written and tested but not
   wired in. Wiring it is the next piece of work, not part of the wire
   migration.

## Areas of uncertainty

1. **Name-version grammar.** Unspecified, as above. Left permissive on purpose.
   If the spec owner wants a grammar, `ValidateNameVersion` is the one place to
   put it and the tests documenting the current permissiveness will fail
   loudly, which is the intent.
2. **`internal/peer.validName` now duplicates a weaker version of
   `proto.ValidateNameVersion`.** Not consolidated in this commit to keep it
   scoped to proto; `peer` should delegate to `proto` in a follow-up.
3. **Intermediate commits do not build.** A wire-contract change cannot be
   half-applied, so `go build ./...` is red from commit 1 until commit 5. Each
   commit is individually reviewable and its own package's tests pass at that
   commit. The AGENTS.md gate is run against the branch tip, not each commit,
   and is green there.
4. **THE SERVING PORT / FACADE PORT PROBLEM — needs a decision.**
   `config.DaemonConfig` has one `ListenAddr`, but three ports are wanted:
   the daemon's own HTTP port, the peer-transfer port that gets announced as
   `servingPort`, and a loopback-only port for the mirror facade (which pkg
   dials, and which must not be reachable off-box). `daemon.servingPort`
   currently derives the announced port from `ListenAddr` — faithful to what
   the pre-v0.2 code did, but it conflates the first two and does nothing for
   the third. This is why `Facade.ListenAndServe` is still not mounted. It is a
   config-schema question (UC-01) and I have not invented an answer.
5. **`PackageHashes` still has no implementation.** Unchanged by this work, but
   it remains the facade's other hard blocker: with `Hashes == nil` every
   package request 404s. It needs a reader for pkg's repository database, whose
   location and format are not in `docs/`. Already on the prior agent's
   uncertainty list in `claude-mirror-facade.md`.
6. **`peer.Server.Serve` still hot-spins on a permanent `Accept` error.**
   Untouched here. `facade_test.go` deliberately leaks a listener to work
   around it.
