# How to demo this — from one process to two machines

Every demo in the project, in one place, ordered by what it costs to run.

**Everything in §1, §2 and §3 was run on 2026-08-10** and the transcripts are
what came back, with one edit: **IP addresses are replaced by placeholders**,
because this repository is public. `$H` is the FreeBSD host (the owner holds the
address — see HANDOFF §7), `<host-ip>` is its public address and `<peer-ip>` is
the other machine's. Nothing else is retouched, including the parts that show a
mistake.

Anything not yet run by anyone says so in its own heading. **Every section here
has now been run**, including the two-box trial (§3.4); what remains untested is
listed explicitly at §3.4.1 rather than left to inference.

| Demo | Needs | Time | Proves |
|---|---|---|---|
| §1.1 `cmd/demo` | nothing | 5s | the peer wire, both ends, constant memory |
| §1.2 the tracker | nothing | 1min | registration, the 3-peer cap, expiry-by-deregistration, the load-bearing `404` |
| §1.3 gate + fuzzer | nothing | 1min | the suite, the race detector, the seeder's HTTP surface |
| §2 one FreeBSD host | the host | 10min | **the whole system against real pkg**, up to and including `pkg install` |
| §3 two machines | + a second box | 15min | a peer that is not `127.0.0.1`: real addresses, a hostile peer, 98 MB at constant memory |

## §1 — No FreeBSD, no pkg, no second machine

### 1.1 The peer wire in one process

```sh
go run ./cmd/demo
```

```
package nginx-1.24.0_2: 43 bytes, sha256 09e4a864fcaf44ce1b1f1d6124d0066bd1a4fe64f06aef824a0d84ef226e5f7b
peer: seed server listening on 127.0.0.1:35209 (max concurrent seeds: unlimited, per IP: unlimited)
peer: served "nginx-1.24.0_2" to 127.0.0.1 (43 bytes, streamed from the cache)
downloaded and verified 43 bytes into /tmp/jmj-1936183540.pkg: "pretend this is a real .pkg archive payload"
peer: 404 for 127.0.0.1: "notheld-1.0" is not held
not-held package correctly refused: peer: fetch 127.0.0.1:35209: peer: remote returned an error: HTTP 404
peer-to-peer transfer succeeded over the v0.2 HTTP wire
seeder stopped: http: Server closed
```

**What it proves.** Everything here except the cache and the expected
hash/size is production code: `peer.Server` on an `http.Server`,
`daemon.CacheSource` as the `PackageSource`, `peer.FetchFromPeer` as the
requester. The bytes cross a real TCP connection, the seeder streams from an
open file handle and the requester spools to `temp_dir` and hashes
incrementally — neither end ever holds the package (`AGENTS.md`'s constant-memory
constraint). The `404` for a package this seeder does not hold is the UC-06 §5b
path.

**What it does not prove.** No tracker, no facade, no pkg, one process, one
machine, 43 bytes. It is the wire, not the system.

### 1.2 The tracker, with `curl`

`trac` is pure Go and runs anywhere. Start it, then drive all three endpoints.
The `--interface` flag is what makes this interesting on one machine: the tracker
keys peers by **the connection's source address** (`cmd/trac/main.go`'s
`clientIP`, and `internal/tracker/tracker.go`), so four loopback addresses are
four distinct peers.

```sh
go run ./cmd/trac                       # :8080

curl -X POST localhost:8080/announce --interface 127.0.0.1 \
  -d '{"servingPort":9002,"packages":["nginx-1.24.0_2","tree-2.3.2"]}'
curl -X POST localhost:8080/announce --interface 127.0.0.2 \
  -d '{"servingPort":9002,"packages":["nginx-1.24.0_2"]}'
curl -X POST localhost:8080/announce --interface 127.0.0.3 \
  -d '{"servingPort":9100,"packages":["nginx-1.24.0_2"]}'
curl -X POST localhost:8080/announce --interface 127.0.0.4 \
  -d '{"servingPort":9002,"packages":["nginx-1.24.0_2"]}'
```

Four holders, and the reply caps at three (`protocol-spec-v0.1.md`: *IWant
returns correct matches, caps at 3*):

```
$ curl 'localhost:8080/peers?pkg=nginx-1.24.0_2'
{"peers":[{"ip":"127.0.0.1","port":9002},{"ip":"127.0.0.2","port":9002},{"ip":"127.0.0.3","port":9100}]}

$ curl 'localhost:8080/peers?pkg=tree-2.3.2'
{"peers":[{"ip":"127.0.0.1","port":9002}]}

$ curl 'localhost:8080/peers?pkg=never-1.0'
{"peers":[]}                        # a miss is 200 with an empty array, never a 404
```

The ping split, which is the whole self-healing mechanism:

```
$ curl -X POST localhost:8080/ping --interface 127.0.0.2      →  200 {"status":"ack"}
$ curl -X POST localhost:8080/ping --interface 127.0.0.9      →  404 {"status":"unknown"}
```

**That `404` is load-bearing** — it is the protocol's *requestPackageList*
message, and a daemon receiving it announces rather than retrying. Restart the
tracker and every peer re-registers on its next ping, with no other mechanism.

An empty announce deregisters, and the fourth holder takes the freed slot:

```
$ curl -X POST localhost:8080/announce --interface 127.0.0.2 -d '{"servingPort":9002,"packages":[]}'
{"status":"ack"}
$ curl 'localhost:8080/peers?pkg=nginx-1.24.0_2'
{"peers":[{"ip":"127.0.0.1","port":9002},{"ip":"127.0.0.3","port":9100},{"ip":"127.0.0.4","port":9002}]}
```

Malformed input, all of it survivable:

```
$ curl -X POST localhost:8080/announce -d 'not json'   →  400  malformed request
$ curl 'localhost:8080/peers'                          →  400  missing pkg parameter
$ curl 'localhost:8080/peers?pkg=../../etc/passwd'     →  200  {"peers":[]}
```

The last one is **not** a hole and is worth understanding before someone "fixes"
it: a name-version reaching the tracker is a map key and a log field, never a
path, so the tracker bounds its length and strips control characters and is done.
The path-safety boundary lives where paths are actually built —
`internal/daemon/cachesource.go` — and `peer.validName` deliberately is not one
either. Adding path validation here would spread the responsibility across three
files without moving it.

The tracker's own log for the whole run:

```
tracker: announce ip=127.0.0.1 port=9002 packages=2
tracker: announce ip=127.0.0.2 port=9002 packages=1
tracker: announce ip=127.0.0.3 port=9100 packages=1
tracker: announce ip=127.0.0.4 port=9002 packages=1
tracker: query pkg="nginx-1.24.0_2" -> 3 peers
tracker: query pkg="tree-2.3.2" -> 1 peers
tracker: query pkg="never-1.0" -> 0 peers
tracker: ping ip=127.0.0.2
tracker: ping from unknown ip=127.0.0.9
tracker: announce ip=127.0.0.2 packages=0 (deregistered)
tracker: bad announce from ::1: proto: decode: invalid character 'o' in literal null (expecting 'u')
```

### 1.3 The gate, the race detector and the fuzzer

```sh
go build ./... && go vet ./... && go test ./... && gofmt -l .
go test ./... -race -count=2                     # required before a merge request
go test ./internal/peer/ -run FuzzSeederHTTPSurface -fuzz FuzzSeederHTTPSurface -fuzztime 30s
```

The fuzzer aims arbitrary bytes at the seeder's HTTP surface end to end — the
obligation `internal/peerwire` used to carry. 30 seconds is enough to see it
working, and it is the cheapest evidence that a hostile peer cannot crash the
serving side:

```
fuzz: elapsed: 30s, execs: 1235362 (38657/sec), new interesting: 74 (total: 220)
PASS
ok  	github.com/ndrew222/p2p-pkg-daemon/internal/peer	31.091s
```

### Why there is no full local swarm demo here

A daemon needs a real SQLite catalogue under `repo_db_dir` — that is where every
expected hash and exact size comes from, and there is no other source. **Nothing
in this repository synthesises one**, so a jmj instance cannot be stood up on a
machine that has never run pkg without copying a catalogue from one that has
(which is exactly what §3 does). Writing a catalogue generator would make a local
swarm demo possible; it would also be new code with no spec behind it, so it has
not been done.

## §2 — One FreeBSD host, end to end — run 2026-08-10

**This modifies the host**: it adds a pkg repository, causes pkg to write a
catalogue under `/var/db/pkg/repos/jmj/`, and really installs a package. §2.8
undoes all of it and verifies the result.

### 2.1 Cross-compile and ship

No toolchain on the host, and none needed. `modernc.org/sqlite` is pure Go and
`fsnotify` has a kqueue backend, so `CGO_ENABLED=0` covers the whole module:

```sh
GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/jmj  ./cmd/jmj
GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/trac ./cmd/trac
ssh $H 'mkdir -p /root/jmjdemo'
scp /tmp/jmj /tmp/trac $H:/root/jmjdemo/
```

### 2.2 Generate a config and start both daemons

`-upstream` is the one required flag and the URL is what decides which
repository pkg ends up installing from. Quote it: `${ABI}` is expanded by jmj at
startup, not by your shell.

```sh
ssh $H 'cd /root/jmjdemo && mkdir -p tmp && ./jmj -generate-config \
  -upstream "https://pkg.FreeBSD.org/\${ABI}/quarterly" \
  -tracker http://127.0.0.1:8080 \
  -facade-addr 127.0.0.1:9001 \
  -serving-addr 0.0.0.0:9002 \
  -temp-dir /root/jmjdemo/tmp \
  -cache /var/cache/pkg \
  -repo-db /var/db/pkg/repos > config.json'
```

```json
{
  "tracker_url": "http://127.0.0.1:8080",
  "facade_addr": "127.0.0.1:9001",
  "serving_addr": "0.0.0.0:9002",
  "temp_dir": "/root/jmjdemo/tmp",
  "cache_dir": "/var/cache/pkg",
  "repo_db_dir": "/var/db/pkg/repos",
  "upstream_url": "https://pkg.FreeBSD.org/${ABI}/quarterly",
  "max_concurrent_seeds": 0,
  "max_concurrent_seeds_per_ip": 0
}
```

`${ABI}` is still a placeholder **in the file** — it is resolved on the machine
that runs the daemon, so a config generated anywhere stays correct anywhere.

```sh
ssh $H 'cd /root/jmjdemo && (nohup ./trac > trac.log 2>&1 &) && sleep 1 && (nohup ./jmj -config /root/jmjdemo/config.json > jmj.log 2>&1 &)'
```

```
daemon: loaded 239 package(s) from /var/db/pkg/repos/FreeBSD-ports-kmods/db
daemon: loaded 37813 package(s) from /var/db/pkg/repos/FreeBSD-ports/db
Repository database: 38052 packages from /var/db/pkg/repos
Discovery started: tracker=http://127.0.0.1:8080, servingPort=9002, cache=/var/cache/pkg
HTTP server listening on 127.0.0.1:9001
peer: seed server listening on [::]:9002 (max concurrent seeds: unlimited, per IP: unlimited)
discovery: announced port=9002 packages=4
```

**`packages=4`, out of 20 in the cache.** That is not a bug and it is the most
important number on this page — see §3.5.

### 2.3 Point pkg at it, keeping the stock repositories

ADR-007: jmj fronts *one* repository and coexists with the rest. A block under
`/usr/local/etc/pkg/repos/` **shadows by name**, so calling it `jmj` adds a
repository while calling it `FreeBSD-ports` would replace the stock one — get
that wrong and the host has no working package manager until you delete the file.

```sh
ssh $H 'mkdir -p /usr/local/etc/pkg/repos && cat > /usr/local/etc/pkg/repos/jmj.conf' <<'EOF'
jmj: {
  url: "http://127.0.0.1:9001",
  mirror_type: "none",
  signature_type: "fingerprints",
  fingerprints: "/usr/share/keys/pkg",
  priority: 100,
  enabled: yes
}
EOF
```

`mirror_type: "none"` is deliberate. `http` is the mechanism that would fit
daemon-first mirror ordering and it **segfaults pkg 2.7.5** — see §4.

### 2.4 `pkg update` — the metadata relay (ADR-005)

```
$ ssh $H 'pkg update -r jmj'
Updating jmj repository catalogue...
Fetching meta.conf: . done
Fetching data: .......... done
Processing entries: .......... done
jmj repository update completed. 37813 packages processed.
```

```
facade: relayed 200 OK for /meta.conf (179 bytes)
facade: relayed 200 OK for /data.pkg (11238229 bytes)
```

pkg accepted a daemon-proxied catalogue as a genuine signed repository:
37,813 packages, signature checking on. Run it again and the conditional `GET`
is relayed, with upstream's `304` passed straight back — never synthesised,
because the daemon tracks no upstream modification times:

```
$ ssh $H 'pkg update -r jmj'
jmj repository is up to date.

facade: relayed 304 for /meta.conf
facade: relayed 304 for /data.pkg
```

### 2.5 The two catalogue behaviours you can watch here

Configuring jmj as a repository makes pkg write `/var/db/pkg/repos/jmj/db` —
**inside the directory jmj itself scans**. The reload after that shows ADR-010
working:

```
daemon: loaded 37813 package(s) from /var/db/pkg/repos/jmj/db          <- ours, first
daemon: loaded 239 package(s) from /var/db/pkg/repos/FreeBSD-ports-kmods/db
daemon: loaded 37813 package(s) from /var/db/pkg/repos/FreeBSD-ports/db
Repository database reloaded: 38052 packages from /var/db/pkg/repos
discovery: announced port=9002 packages=4
```

Two things to notice. Our own catalogue is merged **first**, so its rows win
every collision — pkg resolved the package from the jmj repository, so jmj's row
is the one the bytes must match. And there is **no collision line at all**,
though all 37,813 name-versions collide with `FreeBSD-ports`: only genuine
disagreement in `cksum` or `pkgsize` is reported now (HANDOFF §4.10).

Then §4.9, which you can measure directly:

```sh
ssh $H 'grep -c "Repository database reloaded" /root/jmjdemo/jmj.log'   # before
ssh $H 'pkg update -f -r jmj > /dev/null'                               # ~18s
sleep 6
ssh $H 'grep -c "Repository database reloaded" /root/jmjdemo/jmj.log'   # after
```

```
reloads before: 2
08:10:12 start
08:10:30 pkg update -f finished
08:10:36 settled
reloads after:  3
delta:          1
```

**Exactly one reload for a full catalogue rewrite.** Before the §4.9 fix this
was two: pkg takes the lock and writes `meta` eleven seconds before it writes
anything we read, and the two-second settle fired inside that silence.

### 2.6 The four facade outcomes

```sh
ssh $H 'fetch -qo /tmp/hit.pkg  http://127.0.0.1:9001/All/fish-4.6.0_2.pkg'      # peer hit
ssh $H 'fetch -qo /tmp/miss.pkg http://127.0.0.1:9001/All/xxd-9.2.0738.pkg'      # peer miss
ssh $H 'fetch -o /dev/null http://127.0.0.1:9001/All/nosuchpackage-1.0.pkg'      # 404
ssh $H 'fetch -o /dev/null http://127.0.0.1:9001/All/not-a-valid-stem.pkg'       # 400
```

```
discovery: query pkg="fish-4.6.0_2" -> 1 peers
peer: served "fish-4.6.0_2" to 127.0.0.1 (4842922 bytes, streamed from the cache)
peer: fetched "fish-4.6.0_2" from 127.0.0.1:9002 (4842922 bytes, verified)
facade: served "fish-4.6.0_2" from a peer (4842922 bytes)

discovery: query pkg="xxd-9.2.0738" -> 0 peers
facade: "xxd-9.2.0738": no peer holds it yet; going to upstream
facade: served "xxd-9.2.0738" from upstream (20350 bytes)

facade: "nosuchpackage-1.0": not in the repository database          -> 404 Not Found
                                                                     -> 400 Bad Request
```

Three details worth pausing on:

- **The peer hit's SHA-256 begins `6f428aecbd`** — which is exactly the `~hash10`
  suffix pkg gave the cached file. That is an independent check that verification
  used the right expected value, not merely that some hash matched.
- **`xxd` is in the cache and still went upstream.** The cached copy is 20,321
  bytes; the catalogue says 20,350 after a repository rebuild, so `SanityFilter`
  refuses to announce it and the bytes served are the catalogue's, not the stale
  file's.
- **`404` and `400` are different answers to different questions**: absent from
  the catalogue, versus a path that is not a valid name-version at all. The `400`
  is deliberate — a `404` would carry UC-06 §5b's re-announce obligation, and a
  malformed path is no evidence about what this daemon holds (HANDOFF §5.3;
  ruled canonical 2026-08-10).

`temp_dir` is empty after all four. No spool survives a request.

### 2.7 A real install, and the loop closing on its own

```
$ ssh $H 'pkg install -y -r jmj tree'
New packages to be INSTALLED:
	tree: 2.3.2 [jmj]
[1/1] Fetching tree-2.3.2: ........ done
[1/1] Installing tree-2.3.2...
exit 0
```

```
facade: "tree-2.3.2": no peer holds it yet; going to upstream
facade: served "tree-2.3.2" from upstream (63677 bytes)
discovery: announced port=9002 packages=5          <- the cache watcher, unprompted
tracker: announce ip=127.0.0.1 port=9002 packages=5
```

Nobody in the swarm had `tree`, so it came from upstream — **the first-of-its-kind
install, which under the pre-ADR-003 model returned an error and ended the
install.** Then the cache watcher saw pkg write the package and re-announced
without being asked, which is UC-02 §11: installing made this host a seeder.

### 2.8 Teardown, and verifying it

```sh
ssh $H 'pkill -x jmj; pkill -x trac; \
        pkg delete -y tree; \
        rm -f /usr/local/etc/pkg/repos/jmj.conf; rmdir /usr/local/etc/pkg/repos /usr/local/etc/pkg; \
        rm -rf /var/db/pkg/repos/jmj /root/jmjdemo; \
        rm -f /var/cache/pkg/tree-2.3.2.pkg /var/cache/pkg/tree-2.3.2~*.pkg; \
        pkg update -f'
```

Then check it, with read-only commands, because a teardown script exiting 0 is
not evidence:

| Check | Expected | Got |
|---|---|---|
| `pgrep -x jmj` / `pgrep -x trac` | 0 / 0 | 0 / 0 |
| `/usr/local/etc/pkg` | absent | absent |
| `ls /var/db/pkg/repos` | the stock two | `FreeBSD-ports FreeBSD-ports-kmods` |
| `pkg info | wc -l` | 18 | 18 |
| `ls /var/cache/pkg | wc -l` | 40 | 40 |
| `pkg update` | works | works |
| `pkg install -n tree` | resolves from `FreeBSD-ports` | `tree: 2.3.2 [FreeBSD-ports]` |

**Two traps, both hit while writing this.**

`pkill -f jmjdemo/trac` matches nothing — the daemons were started as `./trac`
and `./jmj -config …`, so the pattern never appears in the command line. Use
`pkill -x`. The first teardown here reported success with both daemons still
running, and only `ps ax` caught it.

`pkg install -n` **exits 1 when it would install something**. That is the dry
run answering "yes", not a failure; read its output rather than its status.

## §3 — Two machines — run 2026-08-10, and honestly labelled

The fetch direction is real and was measured. What is *not* covered is in §3.4.

### 3.1 Where the tracker goes

`trac` is portable by requirement — `AGENTS.md` forbids the tracker any FreeBSD
dependency, and it is pure Go — so it runs on your laptop, a Linux box, or the
peer itself. That is a statement about *portability*, not about placement:

**every peer must be able to reach the tracker, so put it somewhere with a
public address.** A tracker at home behind NAT needs a forwarded port, and that
is one more variable in a trial whose whole point is addressing. In the run
below it sits on the FreeBSD host purely because that box has a public IP.

Two rules the code has already fixed, both in `internal/tracker/tracker.go`:

- **Peers are keyed by the connection's source IP**, never by anything in the
  body — the announce carries only `servingPort`. So peers register under
  whatever address the tracker sees, with no configuration.
- **One daemon per public IP.** Two daemons behind the same NAT overwrite each
  other's entry. Use two separate hosts, not two daemons on one.

Point each daemon's `tracker_url` at the tracker's **public** address even when
it runs on the same box — announcing to `127.0.0.1` registers the peer as
`127.0.0.1`, and every other peer will then dial itself.

### 3.2 A transfer between two machines, with real addresses

FreeBSD host: tracker on `:8080`, daemon serving `0.0.0.0:9102` out of the real
pkg cache. Linux box: a daemon with an **empty** cache and a copy of the
catalogue, so it holds nothing and must fetch.

```sh
# Linux side needs a catalogue; copy one from a machine that runs pkg (read-only).
mkdir -p xhost/repos/FreeBSD-ports xhost/cache xhost/tmp
scp $H:/var/db/pkg/repos/FreeBSD-ports/db xhost/repos/FreeBSD-ports/db    # 73 MB, ~7s

go build -o /tmp/jmj-linux ./cmd/jmj
/tmp/jmj-linux -generate-config \
  -upstream 'https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly' \
  -tracker http://<host-ip>:8080 \
  -facade-addr 127.0.0.1:9001 -serving-addr 0.0.0.0:9002 \
  -temp-dir xhost/tmp -cache xhost/cache -repo-db xhost/repos > xhost/config.json
```

Use a **literal** upstream URL off FreeBSD: `${ABI}` would make jmj run
`pkg config abi`, and there is no pkg on a Linux box. A literal URL never shells
out, which is why the daemon runs there at all.

The tracker, holding two peers at two real public addresses:

```
tracker: announce ip=<host-ip> port=9102 packages=4
tracker: announce ip=<peer-ip> port=9002 packages=0
```

Then, on the Linux box, through its own facade:

```
$ curl -o fish.pkg -w '%{http_code} %{size_download} bytes %{speed_download} B/s\n' \
       http://127.0.0.1:9001/All/fish-4.6.0_2.pkg
200 4842922 bytes 7939060 B/s

$ sha256sum fish.pkg
6f428aecbd706bc9eaa44277825ddd59a453f21cd97f46a9dd45d5b9ce24235a
```

```
# Linux
discovery: query pkg="fish-4.6.0_2" -> 1 peers
peer: fetched "fish-4.6.0_2" from <host-ip>:9102 (4842922 bytes, verified)
facade: served "fish-4.6.0_2" from a peer (4842922 bytes)

# FreeBSD
peer: served "fish-4.6.0_2" to <peer-ip> (4842922 bytes, streamed from the cache)
```

**4,842,922 bytes across the public internet, between two operating systems,
verified against a catalogue hash, at 7.9 MB/s.** The SHA-256 again begins
`6f428aecbd`, matching the `~hash10` suffix on the seeder's cached file.

Drop the fetched package into the Linux daemon's cache and its watcher announces
it, from behind NAT, to a tracker on another machine:

```
discovery: announced port=9002 packages=1
```

### 3.3 ADR-001's asymmetry, measured for the first time

The Linux box is behind NAT: reachable *out*, not *in*. ADR-001 predicts it can
fetch but will never seed, and that this costs the swarm one wasted attempt
rather than a failure. Restarting the FreeBSD daemon against an empty cache
leaves the NAT'd peer as the only holder of `fish`:

```
$ fetch -qo - "http://<host-ip>:8080/peers?pkg=fish-4.6.0_2"
{"peers":[{"ip":"<peer-ip>","port":9002}]}

$ time fetch -qo /tmp/x.pkg http://127.0.0.1:9101/All/fish-4.6.0_2.pkg
08:16:02 start
08:16:08 done              # 6 seconds
exit 0, 4842922 bytes, sha256 6f428aecbd…
```

```
discovery: query pkg="fish-4.6.0_2" -> 1 peers
peer: fetch from <peer-ip>:9002 failed: … dial tcp <peer-ip>:9002: i/o timeout
facade: "fish-4.6.0_2": 1 peer(s) tried, none served a verified copy: … going to upstream
facade: served "fish-4.6.0_2" from upstream (4842922 bytes)
```

Everything the design says should happen, happened: the unreachable peer was
tried, the 5-second `dialTimeout` bounded the attempt, the fetch fell through to
upstream, the caller got correct bytes, and **the peer was not blacklisted** —
only a hash mismatch blacklists, never a dial failure. Cost of a NAT'd peer in
the swarm: five seconds, once, to whoever tries it.

### 3.4 The full trial, on two public boxes — run 2026-08-10

A second box closed everything this section used to list as untested. Full log,
including the setup that would have proved nothing and had to be redone:
**`docs/logs/claude-two-machine-trial.md`**.

| Was untested | Result |
|---|---|
| Selection among several holders | Two holders offered, first taken, second never contacted. Peer order is a **map iteration — effectively random per query**; do not build a test that assumes it. |
| A dialable but hostile peer | Same-size forgery served; requester rejected it on hash, **blacklisted the peer**, and the caller still got the correct package from upstream. |
| Whole-peer blacklisting | The next request — a package that peer held *honestly* — was skipped without a dial. |
| Recovery inside the swarm | With a second holder present: corrupt peer blacklisted, **retried, served from the swarm**, upstream never contacted. |
| Both directions | Each box served and was served, 21 seconds apart. |
| Size and latency | **98,852,086 bytes in 2.27s (~43.5 MB/s)**, and RSS moved **+20 KiB on the requester, 0 on the seeder**. The constant-memory constraint, measured on a real transfer for the first time. |
| A tracker separate from both peers | Tracker-only box, its own registration expired after the 60s `Timeout`, transfer between the other two. |
| ADR-002's `503` (bonus) | Cap of 1: the big transfer held the slot, the second request was refused instantly with no queueing, the requester advanced to upstream. **Both callers got 200.** |

**The recipe, as run.** Two public FreeBSD instances plus any third machine as
the fetcher:

```sh
# on each box: same jmj config, tracker pointed at the TRACKER'S PUBLIC ADDRESS
./jmj -generate-config -upstream 'https://pkg.FreeBSD.org/${ABI}/quarterly' \
  -tracker http://<tracker-ip>:8080 \
  -facade-addr 127.0.0.1:9101 -serving-addr 0.0.0.0:9102 \
  -temp-dir /root/jmjt/tmp -cache /var/cache/pkg -repo-db /var/db/pkg/repos > config.json

# prime a shared set FRESH on both boxes -- cached copies decay against the
# catalogue (§3.5), and two stock hosts start with no announceable overlap
pkg fetch -y curl git

# who holds what, from anywhere
curl 'http://<tracker-ip>:8080/peers?pkg=curl-8.21.0'

# fetch through a box's own facade; the peer wire does the rest
fetch -qo /tmp/x.pkg http://127.0.0.1:9101/All/gcc14-14.2.0_6.pkg
```

Point `tracker_url` at the tracker's **public** address even on the box running
the tracker: announcing to `127.0.0.1` registers the peer as `127.0.0.1`, and
every other peer then dials itself.

To reproduce the hostile-peer result, copy a package into a daemon's cache
directory and overwrite one byte **without changing the length** — `SanityFilter`
compares sizes, so a same-size forgery is announced normally and only the
requester's hash check catches it. Curate holder sets so exactly one path is
possible; with two honest holders and random ordering, a test can pass without
ever exercising what it claims to.

### 3.4.1 What is still not covered

Small and worth stating rather than implying completeness:

- **A slow link.** Both boxes are well-connected; 98 MB moved in 2.27s. The
  deliberate absence of stall detectors and transfer deadlines is still untested
  against a genuinely slow or lossy peer — which is the condition it exists to
  tolerate.
- **An interrupted transfer.** Nothing has been killed mid-stream, so
  resume-after-interrupt — the one place a `Range` request would plausibly appear
  — remains unobserved (§7.3's caveat).
- **More than three holders.** `MaxPeers` is 3 and was never saturated.

### 3.5 A bound worth knowing before you build a swarm

The host announced **4 of the 20 packages in its cache**, twice — once in the
2026-08-09 round and again unprompted today. The other 16 have a `pkgsize` that
no longer matches the catalogue: the repository was rebuilt upstream and
`pkgsize`/`cksum` moved under an unchanged name-version. `SanityFilter` drops
them, which is correct — announcing them would have peers fetch bytes that cannot
verify.

The consequence is a property of the system, not a defect: **a host's shareable
set decays as the repository is rebuilt**, and what stays shareable is what was
fetched most recently. A swarm is therefore worth most in the burst case — many
hosts installing the same thing in the same window — and worth least as a
long-lived archive of everything anyone ever downloaded. Nothing in the design
documents anticipates this, and no ruling has been asked for; it is recorded here
as a bound rather than a question.

## §4 — The finding demo: pkg does not fall through

Already written, and not repeated here:
**`docs/logs/claude-pkg-mirror-verification.md` §"How to demo it"** reproduces
§7.1 in about five minutes — a facade that answers `200` versus one that answers
`404`, with a healthy repository holding the package the whole time. It is the
measurement that produced ADR-003 and therefore the reason the facade proxies
upstream at all.

Two warnings carried from it: use its mechanism (3), which adds a repository and
leaves the stock ones working; and pick a package that is genuinely absent, since
an already-installed one makes pkg a no-op and the demo silently proves nothing.

**Do not demo `mirror_type: http`.** It is the one mirror-ordering mechanism
that fits daemon-first, and it segfaults pkg 2.7.5 — with jmj not running, with
`signature_type: none`, and against a stock `python3 -m http.server`. That is a
gap in FreeBSD's own infrastructure rather than anything this project can fix;
the complete, fileable report is
`docs/logs/freebsd-bug-report-pkg-mirror-type-http.md`.
