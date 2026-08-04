# Keep-Alive (UC-05)

Daemon-side liveness loop. Keeps this daemon on the tracker's peer list while it
holds packages, and cleanly drops off when it doesn't.

Location: `internal/discovery/keepalive.go`, together with the tracker client it
depends on.

## Why is it needed

The tracker holds peers in an in-memory map with a 60s lease (`LeaseTTL` in
`client.go`). An entry expires unless it's refreshed. A daemon that crashes or drops off the network is detected by
*silence*: it stops refreshing, its lease runs out, the tracker deletes it.

Keep-alive is this daemon's side of that: a loop that refreshes the lease every
`PingInterval` (20s). Three refreshes per lease means one or two dropped pings
won't get us wrongly evicted.

Only a daemon that *holds* packages needs to be listed. An empty cache means
nothing to serve, so the daemon shouldn't be listed and shouldn't ping at all.

## Structure

One goroutine (`Run`) owns all state and reacts to three events via `select`:

| event         | trigger            | handler                               |
|---------------|--------------------|---------------------------------------|
| pinger fires  | every 20s          | `ping()` — send one keep-alive ping   |
| cache changed | watcher signals    | `announce()` — push full package list |
| shutdown      | `done` closed      | stop ticker and return                |

One goroutine handling all three means `ping()` and `announce()` never run
concurrently, so the state needs no mutex.

The only state is one flag:

- **`registered`** — was my last successful announce non-empty, i.e. am I
  currently on the tracker's list. `ping()` reads it; `announce()` sets it.

## The two handlers

### `announce()` — "this is what I have"
Scans the cache, pushes the whole list to the tracker (always a full
replacement, never just a change [the delta] ), then updates `registered`:

- scan **error** -> do nothing, leave `registered` as it is. A disk error should not
  be mistaken for an empty cache, which would deregister a healthy daemon.
- announce call **fails** -> tracker stored nothing, so `registered` stays
  false. Retry on the next event.
- **success** -> `registered = len(pkgs) > 0`. Non-empty means we're a holder
  (keep pinging); empty (e.g. after `pkg clean`) means the tracker drops us and
  we go quiet.

### `ping()` — "still here"
One beat of the heartbeat:

- **not registered** -> send nothing. Empty cache, nothing to keep alive.
  Pinging here would just 404-loop forever.
- registered, ping **OK** -> lease renewed, tracker remembers you, done.
- registered, ping **404** (`ErrUnknownPeer`) -> tracker forgot us (restarted, or
  our lease lapsed). A ping carries no list and can't restore us, so we
  `announce()` to re-register.
- registered, **network error** -> couldn't reach the tracker (refused/timeout). It most likely still has our entry, so don't re-register, just log it and let the next tick try again.

The 404-vs-network-error split is deliberate: "tracker forgot me" and "network
hiccup" are different problems, so `ping()` checks the specific error with
`errors.Is(err, ErrUnknownPeer)` rather than a plain `err != nil`.

## How it plugs in

`ping()` and `announce()` talk to two small interfaces, not concrete types:

```go
type Tracker interface {
    Ping() error
    Announce(port int, packages []string) error
}
type Cache interface {
    Scan() ([]string, error)
}
```

- `Tracker` is satisfied by `*discovery.Client`, asserted at compile time in
  `client.go`. It was a stand-in until spec v0.2 landed; the interface did not
  have to change to accommodate the real client.
- `Cache` is satisfied by `daemon.cacheSource`, which wraps the cache watcher
  and maps `PackageInfo` to the name-version strings that go on the wire. The
  adapter lives in `daemon` because `discovery` cannot import it without a
  cycle. It must return an *error* on failure, never an empty list (see
  `announce()`) — an empty list is the deregistration path, so reporting a
  failed scan as "no packages" would silently withdraw the daemon.

Writing the loop against interfaces before the wire format was final turned out
to be the right call: the v0.1 semantics these pin were settled, only the
encoding behind them was open, and when v0.2 arrived the loop needed no
changes at all.

## Tests

`keepalive_test.go`, table-driven, with fakes for both interfaces. Eight cases,
one per branch above (KA-01..KA-08). 
**Refer to PM docs for test cases** <br><br>Run:

```
go test -v ./internal/discovery/
```

## Resolved

Both items below were open pending v0.2. Both are now done, and the prediction
held: the loop did not change, only the `Client` behind the interface did.

- **Identity model.** Resolved as predicted — the tracker keys on the
  connection's source IP and there is no `peer_id` anywhere on the wire.
  `proto.PeerID` and `proto.PingRequest` are gone; `Announce` takes
  `(servingPort int, packages []string)` and `Ping` takes nothing, which is
  exactly the `Tracker` interface this document already specified.
- **Wire encoding.** Resolved: HTTP + JSON, `POST /ping` (empty body),
  `POST /announce`, `GET /peers?pkg=`. Success is `200`, not the `204` the v0.1
  client used. The load-bearing `404` on `/ping` still arrives as
  `discovery.ErrUnknownPeer`, so the KA-03 branch is unchanged.

`*discovery.Client` now satisfies this document's `Tracker` interface, asserted
at compile time in `client.go`. See `docs/logs/claude-proto-v0.2.md`.