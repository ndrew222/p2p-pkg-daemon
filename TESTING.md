# Testing

How this system is tested: which layer proves what, which use case each test
belongs to, what the adversarial cases are, and — the section worth reading
before anyone claims coverage — what is deliberately **not** tested.

Orientation only, like `README.md`. Where this file disagrees with a document
in `docs/`, that document wins and this file is the bug.

## What "end to end" means here, and why it is awkward

Three facts shape every decision below.

1. **The real client is `pkg`, and the project may not modify it.** `pkg` is
   never patched, wrapped or stubbed (`AGENTS.md`); the daemon integrates by
   impersonating a mirror over HTTP. So the outermost loop — a user typing
   `pkg install` — can only be exercised on a FreeBSD host with a real `pkg`.
2. **Everything below that loop must run anywhere.** `AGENTS.md` requires the
   tracker and its tests to build and pass with no FreeBSD, no `pkg` and no
   second machine, and in practice the whole suite does: `modernc.org/sqlite` is
   pure Go and `fsnotify` has a kqueue backend, so the module cross-compiles to
   FreeBSD and the tests carry no `testdata` and shell out to nothing.
3. **There are three wires, and they share no path grammar** — daemon↔tracker,
   pkg→daemon (the facade) and daemon↔daemon (the peer wire). A test that
   exercises one proves nothing about the others, which is why the layer table
   below is organised by wire as much as by package.

The consequence: **the automated suite stops at the facade's front door**, and
the last hop — real `pkg` talking to it — is covered by measured manual
procedures rather than by code. That boundary is deliberate and is where most of
the honest limitations in this document live.

## Running it, in cost order

```sh
go build ./... && go vet ./... && go test ./...   # the gate — no CI exists
gofmt -l .                                        # must print nothing
```

```sh
go test ./... -race -count=2      # REQUIRED before requesting a merge; not part of the gate
```

`-race` is called out separately because it has already caught something the
plain gate could not: a change merged green with a data race in the daemon's
discovery start-up path, found only when a later change ran `-race`, and
intermittent even then — two runs in three. Two HTTP servers, two semaphores,
two watcher goroutines and a SIGHUP reload path share this process, so the plain
gate is not sufficient evidence on its own. `-race` needs cgo and therefore
cannot cross-compile to FreeBSD; it stays a Linux gate.

```sh
go test ./internal/daemon/                                   # one package
go test ./internal/daemon/ -run TestFacadeStatusCodes -v     # one test
go test ./internal/peer/ -run FuzzSeederHTTPSurface -fuzz FuzzSeederHTTPSurface -fuzztime 30s
go run ./cmd/demo                                            # the peer wire, both ends, one process
plantuml -checkonly docs/uc-02.puml                          # before committing a diagram
```

Beyond that, the manual procedures: `docs/logs/claude-demo-guide.md` §1 (no
dependencies), §2 (one FreeBSD host), §3 (two and three machines).

**As of 2026-08-10** the gate is green, `gofmt -l .` prints nothing,
`-race -count=2` passes, and the fuzz target survives. The suite is **183 test
functions expanding to 419 cases** across seven packages, and it runs in about
four seconds without `-race`.

| Package | Test functions |
|---|---|
| `internal/daemon` | 79 |
| `internal/config` | 30 |
| `internal/peer` | 28 (including the fuzz target) |
| `internal/tracker` | 12 |
| `internal/discovery` | 12 |
| `internal/proto` | 11 |
| `cmd/trac` | 11 |

## The layers

### 1. Unit — a rule in isolation

Table-driven, per `AGENTS.md`: each spec's *Definition of done* is that
component's test list. Message validation (`internal/proto`), config validation
and expansion (`internal/config`), the peer table and its expiry
(`internal/tracker`), and the two path rules that decide what a request even
*is* — `TestPackageRequest` for the facade's `All/[Hashed/]<name-version>.pkg`
and `TestUpstreamURL` for the containment of the upstream join.

*Cannot prove:* that any of it is wired together.

### 2. Component over a real socket

The same code, but reached through HTTP rather than called directly, so
encoding, status lines and header handling are in the loop:
`TestFacadeOverRealHTTP`, `cmd/trac`'s `TestOverRealSocket` and
`TestLifeCycleOverHTTP`, `internal/peer`'s `TestRoundTrip`, and
`internal/discovery`'s client tests against an `httptest` tracker.

*Cannot prove:* that the daemon mounts these handlers on the addresses it
claims, or that anything reaches the tracker unprompted.

### 3. Integration — components wired as the daemon wires them

These are the ones that cross package boundaries and bind real listeners. They
are the closest thing in the repository to an automated end-to-end test:

| Test | What it wires |
|---|---|
| `TestFacadeIsMountedOnFacadeAddr` | config → `Daemon` → facade on the loopback address, fetched over HTTP |
| `TestSeedServerIsMountedOnServingAddr` | config → `Daemon` → `CacheSource` → `peer.Server` on the public address |
| `TestCacheChangeReachesTheTracker` | a file appearing in `cache_dir` → watcher → sanity filter → announce arriving at a stub tracker |
| `TestCatalogueRewriteReachesTheTracker` | a catalogue rewrite under `repo_db_dir` → `RepoWatcher` → `Repositories.Reload` → re-announce (ADR-008) |
| `TestSeedServer404TriggersAFullReAnnounce` | a peer asking for a package we no longer hold → `404` → full re-announce (UC-06 §5b) |
| `TestRepoWatcherLifetimeIsDiscoverys` | the reload watcher starting and stopping with the keep-alive rather than outliving it |
| `TestRestartMovesTheSeedServer`, `TestStopClosesTheSeedServer` | SIGHUP reload and shutdown releasing the listener deterministically |

*Cannot prove:* anything involving `pkg`, or a peer that is not `127.0.0.1`.

### 4. System — real `pkg`, real machines

Manual, measured, and transcribed in `docs/logs/claude-demo-guide.md`. See
[System end-to-end](#system-end-to-end-the-manual-procedures) below.

### 5. Robustness

- **Fuzzing.** `FuzzSeederHTTPSurface` aims arbitrary bytes at the seeder's HTTP
  surface end to end — request framing, the path rule and the name-version check
  in one target, on the code path a hostile peer actually reaches. It asserts
  only that the server never panics and always terminates the connection. This
  is an obligation inherited from the deleted `internal/peerwire`, deliberately
  widened: the old target fuzzed a framing function in isolation, and framing is
  now the standard library's problem. `TestSeederStillAnswersAfterHostileTraffic`
  pins the recovery half.
- **The race detector**, as above.
- **Malformed input at every entrance.** `TestMalformedInput`,
  `TestOversizedInputIs413`, `TestPanicRecovery` (tracker);
  `TestDecodeMalformed`, `TestDecodeTooLarge` (`proto`);
  `TestRequesterRejections` (peer wire); `TestLoadMovesCorruptConfigAside`,
  `TestLoadRejectsLegacyKeys` (config).

## Use case → evidence

`docs/use-case-descriptions.md` is the behaviour spec. UC-03 and UC-04 are
retired numbers.

| Use case | Automated | System |
|---|---|---|
| **UC-01** Configure the daemon | `internal/config` (30 functions: validation, the loopback rule for `facade_addr`, `${ABI}` expansion, generation round-trip, legacy keys, corrupt-file recovery, the seeding caps); `TestStartHTTPServerRefusesWithoutARepositoryDatabase`; `TestSeedServerRefusesWithoutACacheDir`; `TestRepoWatcherStartRefusesAndCreatesNothing` | guide §2.2 |
| **UC-02** Install via P2P | `internal/daemon/facade_test.go` (22 functions: the status set, the peer path, the upstream path, spool lifetime, blacklist persistence); `internal/peer` fetch and blacklist tests | guide §2.6, §2.7, §3.2, §3.4 |
| **UC-05** Announce and liveness | `internal/tracker` and `cmd/trac` (registration, replacement-not-merge, the 3-peer cap, expiry, the `/ping` `404`, one-daemon-per-IP); `internal/discovery` (client + keep-alive, ping suppression); `watcher_test.go`, `repowatcher_test.go`; the two "reaches the tracker" integration tests | guide §1.2, §2.5 |
| **UC-06** Serve packages | `internal/peer/serve_test.go` (status codes, the caps, slot release, per-IP identity, handle closure); `cachesource_test.go` (the path-safety boundary, read-only, regular files only); `seedserver_test.go`; the fuzz target | guide §1.1, §2.6, §3.4 |
| **UC-07** Repository metadata | `TestFacadeRelaysMetadataFromUpstream`, `TestFacadeRelaysConditionalGetAnd304`, `TestFacadeRelaysUpstreamStatusForMetadata`, `TestFacadeMetadataRelayFailureIs502`, `TestFacadeRelayDoesNotSpoolOrCache`, `TestFacadeRelayDropsHopByHopHeaders`, `TestFacadeRelaysTheUserAgent` | guide §2.4 |

## Adversarial and negative cases

The threat model is explicit: **peers are not trusted, the tracker never
verifies content, and an announce is a claim rather than a fact.** These are the
cases that encode it.

| Case | Expected behaviour | Where |
|---|---|---|
| A peer serves a same-size forgery | Hash mismatch caught, spool deleted, **peer blacklisted**, caller still served | `TestFacadeNeverServesUnverifiedBytes`, `TestFetchFirstBlacklistsCorruptPeer`; measured over a real link, guide §3.4 |
| A peer serves more bytes than expected | Cut off at `expectedSize+1` and rejected, without a global cap | `TestRequesterRejections` |
| `Content-Length` disagrees with the catalogue | Abandoned before a byte of body is read | `TestRequesterRejections` |
| A peer is unreachable | One wasted attempt, next peer tried, and **not blacklisted** — a dial failure is not evidence of dishonesty | `TestUnreachablePeerIsNotBlacklisted`; guide §3.3 |
| A blacklisted peer reappears in a tracker reply | Skipped at selection, without a dial | `TestBlacklistedPeerIsSkippedAtSelection`, `TestFacadeBlacklistOutlivesOneRequest` |
| The seeder is at capacity | `503` immediately: no queueing, no `Retry-After`, and **no re-announce** | `TestSeedingCaps`, `TestRefusalDoesNotReAnnounceAndDoesNotInviteAWait`; guide §3.4 |
| A peer forges `X-Forwarded-For` to dodge the per-IP cap | Ignored; identity is the connection's source address | `TestPerIPCapIgnoresForwardingHeaders`, `TestIdentityIgnoresForwardedHeaders` |
| Many one-shot remote IPs | The per-IP table does not grow without bound | `TestPerIPTableDoesNotGrowWithChurn` |
| Path traversal on any of the three wires | Facade cleans and contains; the seeder's source refuses anything that is not a plain file name; the tracker treats a name-version as a map key and never a path | `TestUpstreamURL`, `TestCacheSourceRefusesAnythingThatIsNotAPlainFileName`, guide §1.2 |
| `temp_dir` is unwritable | Peer path disabled, request served from upstream anyway; `502` only if upstream is gone too (ADR-009) | `TestFacadeUnwritableTempDirGoesToUpstream`, `…WithNoUpstreamIs502` |
| The package is absent from the repository database | `404` — the one surviving one, because nothing can verify or bound it | `TestFacadeStatusCodes` |
| Arbitrary bytes at the seeder | Never a panic; always a terminated connection | `FuzzSeederHTTPSurface` |
| A corrupt or legacy config | Corrupt → moved aside best-effort, defaults used; legacy key → refuse and name the replacement, file untouched | `TestLoadMovesCorruptConfigAside`, `TestLoadRejectsLegacyKeys` |
| The daemon is asked to write where it must not | The cache dir is never created, the repo DB is opened read-only, and the seeder never writes | `TestWatcherStartDoesNotCreateCacheDir`, `TestRepositoriesOpensReadOnly`, `TestCacheSourceNeverWritesToTheCache` |

## System end-to-end: the manual procedures

`docs/logs/claude-demo-guide.md` is the runnable version, with the transcripts
each section produced on the date it names. Summarised here so this document is
a map rather than a copy — **the results below were measured on the dates given,
not re-run for this file.**

### No dependencies (guide §1)

`go run ./cmd/demo` drives the real peer wire in one process: `peer.Server` over
`daemon.CacheSource`, fetched with `peer.FetchFromPeer`, across a real TCP
connection, with a `404` for a package the seeder does not hold. Everything but
the cache and the expected hash is production code. It proves the wire and
nothing above it — no tracker, no facade, no `pkg`, 43 bytes.

§1.2 drives all three tracker endpoints with `curl`, using several loopback
source addresses as several peers, and covers the 3-peer cap, deregistration by
empty announce, and the `/ping` `404` that is the protocol's *requestPackageList*
message rather than an error.

### One FreeBSD host (guide §2, run 2026-08-10)

The whole system against real `pkg`: cross-compile, generate a config, start
`trac` and `jmj`, add jmj as a pkg repository alongside the stock ones, then

- **`pkg update` through the facade** — 37,813 packages processed with
  `signature_type: fingerprints` intact, `If-Modified-Since`/`304` relayed
  (ADR-005 / UC-07);
- **the four facade outcomes** — a peer hit serving 4,842,922 verified bytes off
  the peer wire, a peer miss going to upstream, a `404`, and a `400`;
- **`pkg install -y -r jmj tree` exiting 0**, with the cache watcher then taking
  the announce from 4 packages to 5 unprompted — the loop closing on its own;
- **`temp_dir` empty afterwards**, which is the no-store property being checked
  rather than asserted.

§2.8 tears it all down and verifies the teardown, because "it's clean now" is a
claim and three read-only commands are evidence.

### Two and three machines (guide §3, run 2026-08-10)

Peers that are not `127.0.0.1`, with real public addresses and a tracker that
was, in the last phase, on a box that was neither peer:

- **98,852,086 bytes in 2.27s (~43.5 MB/s)** while the requester's RSS moved
  27,992 → 28,012 KiB (**+20 KiB**) and the seeder's did not move at all. This
  is the measurement that turns `AGENTS.md`'s constant-memory constraint from a
  code review into a property a regression would break.
- **A hostile peer serving a same-size forgery** was caught on hash and
  blacklisted, and the caller was still served correctly — from upstream in one
  arrangement, and from a second honest holder in the other, never touching
  upstream.
- **The blacklist is whole-peer and it bites**: the very next request, for a
  package that peer held honestly, was skipped without a dial.
- **ADR-002's `503` degrades to upstream, not to failure**, under
  `max_concurrent_seeds: 1`.
- **ADR-001's asymmetry costs five seconds**: a NAT'd peer announces normally,
  is dialled, times out, and is *not* blacklisted.

## What is deliberately not tested, and why

The honest section. None of these is an oversight to be quietly closed.

- **There is no automated local full-swarm test.** A daemon needs a real SQLite
  catalogue under `repo_db_dir` — that is the sole source of every expected hash
  and size, and there is no other. Nothing in the repository synthesises one
  outside package-internal test helpers (`writeRepoDB` in `repodb_test.go`, which
  is unexported and used only by `internal/daemon`'s own tests). Building a
  catalogue generator would make a local swarm test possible; it would also be
  new code with no spec behind it, which `AGENTS.md` ground rule 1 forbids. Raise
  it as a spec question first.
- **A genuinely slow link, an interrupted transfer, and more than three
  holders** are uncovered (guide §3.4.1). The interruption case is the one place
  a `Range` request would plausibly appear, and pkg 2.7.5 was measured never to
  send one — so resume-after-interrupt is untested *and* unspecified.
- **`-race` on FreeBSD.** It needs cgo and a FreeBSD C toolchain, so it cannot
  be cross-compiled and stays a Linux gate.
- **Stall detectors, throughput floors and transfer deadlines have no tests
  because they do not exist.** A slow peer is out of scope exactly as a slow
  mirror is (`AGENTS.md`, ADR-001). Do not add a test that assumes one.
- **`cmd/jmj` and `cmd/demo` have no test files.** Both are thin `main`s over
  `internal/`, which is where the behaviour and the tests live.
- **Peer order from the tracker is unspecified and effectively random** —
  `/peers` iterates a Go map. A multi-holder test that assumes an order can pass
  while never exercising the path it claims to; that is exactly how one trial's
  first hostile-peer setup was built and had to be redone. Curate the holder set
  so only one path is possible.
- **A cached package can stop matching its own catalogue entry with no version
  change**, because the repository is rebuilt under the same name-version —
  measured at 16 of 20 cached packages on the reference host. `SanityFilter` is
  what keeps those out of the announce. A test that "optimises" it away would be
  testing for a defect.

## Where the evidence lives

| Document | What it holds |
|---|---|
| `docs/logs/claude-demo-guide.md` | Every demo, ordered by cost, with dated transcripts |
| `docs/logs/HANDOFF.md` §7 | The empirical findings, numbered and cited from ADRs and source |
| `docs/logs/claude-pkg-mirror-verification.md` | Why the facade design changed: pkg does not fall through between repositories |
| `docs/logs/claude-freebsd-host-round.md` | The FreeBSD host round — catalogue behaviour, `fsnotify` measurements, the real install |
| `docs/logs/claude-two-machine-trial.md` | The multi-machine trial, including the hostile peer and the memory measurement |
| `docs/logs/freebsd-bug-report-pkg-mirror-type-http.md` | A `pkg` segfault found by this testing, complete and fileable |
