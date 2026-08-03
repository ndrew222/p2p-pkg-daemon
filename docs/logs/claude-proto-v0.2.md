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

## Areas of uncertainty

1. **Name-version grammar.** Unspecified, as above. Left permissive on purpose.
   If the spec owner wants a grammar, `ValidateNameVersion` is the one place to
   put it and the tests documenting the current permissiveness will fail
   loudly, which is the intent.
2. **`internal/peer.validName` now duplicates a weaker version of
   `proto.ValidateNameVersion`.** Not consolidated in this commit to keep it
   scoped to proto; `peer` should delegate to `proto` in a follow-up.
3. **Intermediate commits do not build.** A wire-contract change cannot be
   half-applied. `go build ./...` is red from this commit until the
   `discovery` commit lands; each commit is individually reviewable and
   `go test ./internal/proto/` passes at this one. The AGENTS.md gate is run
   against the branch tip, not each commit.
