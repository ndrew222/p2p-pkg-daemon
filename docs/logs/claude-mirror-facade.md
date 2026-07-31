# Work log — mirror facade (UC-02 / UC-07)

Author: Claude (agent). Branch: `claude/mirror-facade-impl-ozomgx`.

## How I approached it

The facade is the HTTP surface pkg talks to. It has exactly two jobs: serve a
verified package file, or return an error that makes pkg fall through to its
next mirror. Everything else — peer selection, hash verification, the fetch
loop — already existed in `internal/peer`, so the facade is deliberately thin:
classify the path, look up the expected hash, ask the tracker, try peers, map
each outcome to a status code.

I started blocked. `AGENTS.md` names `docs/tracker-protocol-spec-v0.2.md` as
the authority for all wire-level code and says work stops if it is missing; it
was. The spec owner landed it mid-task (`8089a57`). Reading it, v0.2 turned out
to govern **only** daemon↔tracker — it says nothing about the pkg↔daemon mirror
wire this feature is. So it lifted the blanket block without answering any of
the facade's own questions. I reported that rather than reading the tracker
codes across to a different surface.

Two of those questions were then answered by the owner directly:

- **Path rule** — settled by a worked example
  (`https://pkg.ghostbsd.org/stable/FreeBSD:15:amd64/latest/All/gopls-0.22.0_1.pkg`).
  I generalised it to "the last two segments are `All/<name-version>.pkg`,
  ignore everything before", because the prefix is branch/ABI/repo-name and
  varies per mirror. The generalisation is mine; if it is wrong the fix is one
  function, `packageRequest`.
- **Status codes** — the owner instructed me to choose and document them. I did,
  in `docs/mirror-facade-spec-v0.1.md`, with the rationale for the two
  debatable ones. That document is explicitly marked as implementer-drafted,
  not owner-ratified, so nobody downstream mistakes it for a settled contract.

The example URL also incidentally confirms `.pkg` over `.txz`, which the cache
watcher had flagged as an open question in `watcher.go:22` — worth telling
whoever owns that.

## Difficulties

**Distinguishing two failure modes through `peer.Download`.** `Download`
returns `ErrNoPeers` both when the tracker returned an empty list and when
every peer failed. The mirror surface has to tell those apart — the first is
"this mirror holds nothing" (404), the second "peers claimed it and failed to
deliver" (502), and collapsing them would hide a swarm serving corrupt bytes. I
inlined the peer loop in `servePackage` using `peer.FetchFromPeer` directly and
left `Download` untouched, with a comment saying why. Duplicating a five-line
loop was the lesser evil against changing another feature's exported API.

**Merging three branches.** `feature/uc1-config`, `feature/keepalive` and
`feature-cache_watcher` all touch `cmd/jmj/main.go`; the first two also both
rewrite `RunHeartbeat`. Resolutions:

- `internal/discovery/client.go` — kept uc1-config's `select`/`stopChan` loop
  (`daemon.Reload` calls `client.Stop()`, so it is load-bearing) with
  keepalive's `ticker`→`pinger` rename applied on top. Purely mechanical.
- `cmd/jmj/main.go` — kept uc1-config's entrypoint. The watcher branch's
  `main.go` is a standalone demo harness for the watcher (it calls
  `cachewatcher.New` against a package that is named `daemon`, so it does not
  compile as merged anyway); the watcher library itself is untouched. The
  watcher author already flagged this collision in their commit message.
- `go.mod` gained `fsnotify` — the watcher branch used it without adding it.

## Areas of uncertainty

| Uncertainty | Clarified? | Outcome |
|---|---|---|
| Mirror URL layout / what counts as a package path | **Yes** — asked the owner, who supplied a worked URL | Implemented as the `All/<name-version>.pkg` tail rule. My generalisation from one example is the residual risk. |
| Status codes for each failure | **Yes** — owner instructed me to invent and document them | Chosen and documented in `docs/mirror-facade-spec-v0.1.md`, marked implementer-drafted. |
| Repo DB: location, format, hash algorithm | **No** — raised, not answered | Isolated behind the `PackageHashes` interface, wired with `nil`, which answers 404 to every package request. Assumes hex SHA-256, same assumption already in `peer/fetch.go:46`. **Nothing works end to end until a real reader lands.** |
| Which port the facade listens on | **No** — raised in this log, not asked directly | UC-01 specifies one "listen port"; `config.ListenAddr` is the peer-facing port announced to the tracker as `servingPort`. The facade is pkg-facing and needs a second, loopback port. I did **not** invent a config field. `ListenAndServe(addr)` takes the address from the caller, so wiring is one line once the field exists. **This is why the facade is not mounted in `Daemon.startHTTPServer`.** |
| `HEAD` requests | **No** | Currently 405. The facade cannot answer honestly without doing the whole fetch. UC-07 already says the mirror-ordering behaviour must be settled by an integration smoke test; this belongs in it. |
| `Range` requests | **No** | Ignored; whole file returned with 200. Flagged in the spec doc. |
| Peer transport: UC-02 §8 and `uc-02.puml:39` say "plain HTTP over TCP" to peers, but `internal/peerwire` is custom length-prefixed binary | **No** — flagged twice, still open | Not touched. The facade calls `peer.FetchFromPeer` and inherits whatever the transport is; if the prose wins, the facade is unaffected. |
| Temp buffer directory | **No** | UC-02 §8 says stream into the buffer dir; `FetchFromPeer` buffers in memory and returns `[]byte`. The facade uses those bytes, so `config.BufferDir` is unused by it. Fixing this is a change to `internal/peer`, not the facade. |

## Deliberately not implemented

- **Local blacklist (UC-02 §11c).** No blacklist type exists anywhere in the
  tree. It belongs to the fetch loop, not the mirror surface — a facade-local
  blacklist would be forgotten between requests and would not help the seeding
  side. A hash-mismatching peer is currently logged and skipped for that
  request only. This is a genuine gap against UC-02.

## Bug found in existing code

`internal/peer/serve.go:38-44` — `Server.Serve` logs and continues on *every*
`Accept` error, including permanent ones. On a closed listener it spins in a
hot loop, burning a core and flooding logs. My tests hit it immediately when
they closed a test listener; I worked around it by leaking the listener instead
of fixing another feature's file. The fix is to return on a non-temporary
error. Not mine to make — flagging it.
