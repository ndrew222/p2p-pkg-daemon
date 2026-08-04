## Use Case Descriptions
 
*UC-03 has been dissolved and UC-04 merged into UC-02; the numbers are retired. UC-07 is new.*
 
| UC-01 | Configure P2P Daemon |  |  |
| :---- | ----- | :---- | ----- |
| **Description** | The user configures the P2P daemon by generating a config file: `jmj -generate-config` writes a complete, valid configuration to **stdout**, and the user redirects it wherever they have permission to write. Settings such as the tracker address, the daemon's temp directory, the pkg cache directory and the daemon's **two** listen addresses are supplied as flags; anything not supplied takes its default. The daemon then reads that file at startup and registers with the tracker (UC-05). |  |  |
| **Actors** | Primary | User (operating the local machine) |  |
|  | Secondary | Tracker (receives the registration ping; it is not set up by this use case) |  |
| **Trigger** | User invokes `jmj -generate-config` with configuration arguments, then starts (or reloads) the daemon |  |  |
| **Precondition** | Daemon is installed. The config file may be present, missing, or corrupt — all three are handled. |  |  |
| **Postcondition** | The user holds a complete config file containing the defaults overlaid with the requested flags; the daemon is running with that configuration and is registered with the tracker. |  |  |
| **Error States** | 1 | Invalid setting value (bad port range, malformed tracker address) |  |
|  | 2 | Invalid environment at startup (temp directory not writable, pkg cache directory missing) |  |
|  | 3 | Corrupted configuration file — recoverable, handled inline, not an abort |  |
|  | 4 | Configuration file written against a removed schema (`listen_addr`, `buffer_dir`) — an abort, not a repair |  |
| **Operational Flow** | **Step** | **Action** |  |
|  | 1 | User calls `jmj -generate-config` with flags dictating the desired settings. Flags fully determine the output: what is not supplied takes its default, and the existing config is **not** read as a merge base (see Assumptions) |  |
|  | 2 | jmj validates the values alone — port range, address format, tracker URL, paths non-empty, and `facade_addr` being a loopback address. No filesystem access, so a config can be generated on one machine for another |  |
|  | 3 | jmj prints the complete config as JSON to stdout and exits. **It reads no config file, creates nothing, and writes nothing.** The generator has no side effects and needs no write permission anywhere |  |
|  | 4 | The user redirects the output to a path they can write: `jmj -generate-config > ~/.config/jmj/config.json`, or `jmj -generate-config \| sudo tee /usr/local/etc/jmj.json`. Privilege handling, if any is needed at all, belongs to the shell and never to jmj |  |
|  | 5 | User starts the daemon, or sends SIGHUP to a running one (hot reload; no restart required) |  |
|  | 6 | Daemon reads the config file: readable → those settings, with any absent key taking its default; missing → not an error, defaults throughout; corrupted → see flow c; written against a removed schema → see flow d |  |
|  | 7 | Daemon validates the loaded config against **this** machine: the temp directory is created if absent and probed for writability; the pkg cache directory must already exist and is never created, because it is read-only to the daemon |  |
|  | 8 | Daemon opens its two listeners: the mirror facade on `facade_addr`, which pkg fetches from and which **must** be loopback, and the peer transfer server on `serving_addr`, which other daemons fetch from. Only the serving port is announced |  |
|  | 9 | Daemon announces to the tracker. A first-time configuration is by definition an unknown IP, so this is the registration exchange of UC-05 — announcing directly rather than pinging, since a ping from an unregistered daemon can only earn a 404 |  |
| **Alternative Flow** | **Error State:** Invalid setting value |  |  |
|  | **Step** | **Action** |  |
|  | 2a | Validation fails; error message names the offending field |  |
|  | 3a | Abort with a non-zero exit and nothing on stdout, so a redirect produces an empty file rather than a corrupt config |  |
|  | **Error State:** Invalid environment at startup |  |  |
|  | 7b | The temp directory cannot be created or written, the pkg cache directory does not exist, or `facade_addr` is not a loopback address |  |
|  | 8b | Daemon refuses to start and names the offending field or path. The config file is untouched — the daemon never writes it |  |
|  | **Error State:** Corrupted configuration file |  |  |
|  | 6c | Reading the config returns a parse error |  |
|  | 7c | Daemon comes up on defaults and makes a best-effort attempt to move the file aside to config.bak for inspection. The move is not required to succeed: with no write permission on the config directory the daemon logs it and carries on, because jmj never requires write access to its config path — main flow resumes at step 7 |  |
|  | **Error State:** Configuration file written against a removed schema |  |  |
|  | 6d | The config parses, but carries a key that no longer exists: `listen_addr` (now `facade_addr` plus `serving_addr`) or `buffer_dir` (now `temp_dir`) |  |
|  | 7d | Daemon refuses to start, naming the key and its replacement. Unlike flow c the file is **not** moved aside and defaults are **not** substituted: it is a wrong config rather than an unreadable one, and it still holds settings its owner wants. Silently ignoring the key would start the daemon on default ports with the user's setting discarded, which is the failure this flow exists to prevent |  |
| **Assumptions/ Comments** | The tracker address must be configured before any peer interaction is possible. Defaults are valid by construction, so only user-supplied values need validation. **jmj requires no write privileges to be configured:** the generator emits to stdout and the shell performs the write, which is why there is no permission-error path in the generator and no config writer in the codebase. At runtime the daemon writes only to its own temp directory; everything else it touches (config path, pkg cache, repository database) is read-only, the config path aside from the best-effort .bak move above. **The temp directory is not a store.** It holds one in-flight download at a time, because verification needs the whole file before any byte may reach pkg, and each file is deleted as soon as it has been served; the daemon seeds from the pkg cache, never from here. That is why its default is the OS temp directory rather than a directory of the daemon's own — nothing in it is meant to survive a reboot. **The two listen addresses are not interchangeable:** `facade_addr` is loopback-only and enforced as such, because the facade fetches from the network on behalf of whoever asks and would otherwise be an open relay; `serving_addr` is public by nature, since peers are on other machines, and its port is the one the tracker hands out. **Configuration is not a partial update.** An earlier revision of this use case specified merging the requested flags over the existing config. That cannot work for the primary idiom: `jmj -generate-config > config.json` has the shell truncate `config.json` to zero bytes *before* jmj is executed, so the settings to be merged are already gone by the time jmj could read them. Detecting the case and refusing to print is worse — the file has been truncated either way, and defaults would at least have been a valid config. Flags therefore fully determine the output, which makes redirecting onto an existing config well defined and idempotent. To change one setting on an existing config, edit the file, or re-run the generator with all the flags you want. |  |  |
 
---
 
| UC-02 | Install Package via P2P (download package) |  |  |
| :---- | ----- | :---- | ----- |
| **Description** | When the user installs a package that is not already cached, pkg — not the user — contacts the daemon, which acts as pkg's first HTTP mirror. The daemon asks the tracker for peers, downloads the package from a peer into a temporary buffer, verifies the hash against pkg's repository database, and serves the verified bytes to pkg as an ordinary mirror response. Every failure becomes an HTTP error, which makes pkg fall through to its next configured mirror. |  |  |
| **Actors** | Primary | User (runs `pkg install`; never talks to the daemon directly) |  |
|  | Secondary | pkg (the daemon's actual client), P2P Daemon, Tracker, remote serving daemons (UC-06) |  |
| **Trigger** | `pkg install <packageName-version>` |  |  |
| **Precondition** | Daemon is running and configured as pkg's first mirror. Tracker address is configured. Network connectivity is available. Daemon has read-only access to pkg's repository database. |  |  |
| **Postcondition** | Package is installed. The package file is in pkg's cache, written by pkg itself. The cache watcher announces it to the tracker (UC-05), making this machine a seeder. |  |  |
| **Error States** | 1 | Tracker unreachable (network timeout; an unparseable tracker response is treated the same way) |  |
|  | 2 | Tracker has no peers for the package |  |
|  | 3 | Peer sends corrupt data (hash mismatch) |  |
|  | 4 | All peers exhausted |  |
|  | 5 | Peer unreachable (connection timeout, e.g. peer behind NAT) |  |
| **Operational Flow** | **Step** | **Action** |  |
|  | 1 | User runs `pkg install packageName-version` |  |
|  | 2 | pkg searches its repository database; an unknown name is rejected by pkg itself and the daemon is never involved |  |
|  | 3 | pkg checks its own cache; if the package is already there, pkg installs it directly and the flow ends |  |
|  | 4 | pkg requests the package over HTTP from the daemon, its first mirror; multiple packages are independent requests |  |
|  | 5 | Daemon sends IWant(packageName-version) to the tracker |  |
|  | 6 | Tracker returns a list of peers (IP:port) that have announced the package |  |
|  | 7 | Daemon tries the peers in the order returned, skipping any on its local blacklist |  |
|  | 8 | Loop until downloaded or peers exhausted: daemon issues `GET /pkg/packageName-version` over plain HTTP/TCP to the peer's advertised IP:port and streams the response body into a temporary file, hashing as it goes (peer transfer spec v0.2) |  |
|  | 9 | Daemon compares the streamed byte count and the running hash with the expected size and hash, both read from the same row of pkg's repository database. There is no global transfer size limit — the expected size *is* the limit |  |
|  | 10 | On a match, daemon streams the temporary file to pkg as a mirror response and then removes it; pkg re-verifies, writes the file to its own cache, and installs |  |
|  | 11 | The cache watcher notices the new file in the cache and announces it to the tracker (UC-05) |  |
| **Alternative Flow** | **Error State:** Tracker unreachable |  |  |
|  | **Step** | **Action** |  |
|  | 5a | IWant gets no response from the tracker after a few seconds |  |
|  | 6a | Daemon returns an HTTP error to pkg |  |
|  | 7a | pkg tries its next mirror |  |
|  | **Error State:** Tracker has no peers |  |  |
|  | 6b | Tracker returns an empty peer list |  |
|  | 7b | Daemon returns an HTTP error to pkg |  |
|  | 8b | pkg tries its next mirror |  |
|  | **Error State:** Peer sends corrupt data |  |  |
|  | 9c | The streamed size or the computed hash does not match the repository database |  |
|  | 10c | Remove the temporary file |  |
|  | 11c | Mark the peer untrusted in a local blacklist (local only; never reported to the tracker). Only a size or hash mismatch blacklists — a `404` means the peer no longer holds the file, not that it lied |  |
|  | 12c | Select the next peer and re-enter the loop at step 8 |  |
|  | **Error State:** All peers exhausted |  |  |
|  | 8d | The loop ends with every peer tried and no verified download |  |
|  | 9d | Daemon returns an HTTP error to pkg |  |
|  | 10d | pkg tries its next mirror |  |
|  | **Error State:** Peer unreachable |  |  |
|  | 8e | Connection to the peer times out |  |
|  | 9e | Move on to the next peer in the list |  |
| **Assumptions/ Comments** | The daemon has no package store of its own; it buffers in a temporary directory (configurable, defaulting to the system temp directory) and needs write access only there. The buffer is per-request and ephemeral: it exists because verification needs the whole file before any byte may reach pkg, not because the daemon keeps anything. The "fall back to mirror" outcomes are plain HTTP errors — pkg's native mirror fallback does the rest, so pkg is never modified. Packages are identified by name-version strings; integrity comes solely from pkg's signed repository database, which supplies the expected hash and the expected size from the same row. Transport is plain HTTP over TCP with no NAT traversal (ADR-001); a peer that cannot accept inbound connections costs one timeout and a retry. The peer wire is specified in `peer-transfer-spec-v0.2.md` and is a different surface from the mirror facade, with its own `/pkg/name-version` namespace. **There is no fixed limit on package size:** the transfer is bounded by the exact expected size, which is a tighter anti-abuse bound than any constant and imposes no ceiling. Neither end holds a package in memory, so the largest package in the repository (2.83 GiB) transfers on a 1 GiB host. |  |  |
 
---
 
| UC-05 | Announce Packages & Tracker Liveness (seeding to tracker) |  |  |
| :---- | ----- | :---- | ----- |
| **Description** | The daemon advertises the packages in pkg's cache to the tracker and keeps itself registered through periodic pings. The tracker holds one entry per IP (serving port, package list, timeout); an entry whose timeout expires without a ping is flushed. |  |  |
| **Actors** | Primary | P2P Daemon |  |
|  | Secondary | Tracker, pkg cache (/var/cache/pkg, read-only) |  |
| **Trigger** | Daemon startup (UC-01), which announces directly. Periodic keep-alive timer, which pings. Cache watcher sees a package appear in or leave the pkg cache (whether fetched via P2P or an ordinary mirror), which announces the new full list. |  |  |
| **Precondition** | Daemon is running. Tracker address is configured. Network connectivity is available. The cache may be empty. |  |  |
| **Postcondition** | Tracker holds an up-to-date (IP → serving port, package list) entry for this daemon with a running timeout — or, if the announced list was empty, holds nothing for this daemon. |  |  |
| **Error States** | 1 | Network error mid-announce |  |
|  | 2 | Timeout expiry (tracker side) |  |
| **Operational Flow** | **Step** | **Action** |  |
|  | 1 | Daemon pings the tracker. **At startup the daemon skips straight to step 4 and announces:** a freshly started daemon is unknown to the tracker by definition, so its first ping is a guaranteed 404 followed by the announce anyway. Announcing directly is the same exchange minus a wasted round trip, and an announce is accepted from any IP, solicited or not |  |
|  | 2 | If the tracker knows this IP, it resets the timeout and acknowledges; done — no list is transferred |  |
|  | 3 | If the tracker does not know this IP, it replies requestPackageList() |  |
|  | 4 | Daemon scans the pkg cache (read-only) |  |
|  | 5 | Daemon filters the list with cheap sanity checks: valid name-version filename, file size matches the repository database entry. No hashing — the downloading peer verifies integrity on receipt |  |
|  | 6 | Daemon sends announce(listeningPort, packageList). The serving port must be in the message: the tracker takes the IP from the connection's source address but cannot infer the listening port. The list always replaces the previous one in full — never a delta |  |
|  | 7 | Non-empty list: tracker registers (IP → port, list), starts the timeout, and acknowledges. Empty list: tracker acknowledges but stores nothing; the IP stays unregistered until a non-empty list arrives |  |
|  | 8 | While nothing is registered (empty cache), the daemon suppresses keep-alive pings; there is nothing to keep alive |  |
|  | 9 | When the cache watcher sees the cache change — a package appearing, being removed, or being rewritten — the daemon announces directly without waiting to be asked; an announce from a known IP replaces its entry and resets the timeout. This is how an already-registered daemon publishes an updated list. Installing one package pulls in dozens of dependencies, so the watcher's nudge to the announce loop coalesces: a re-announce already pending absorbs further changes rather than queueing one announce per file |  |
| **Alternative Flow** | **Error State:** Network error mid-announce |  |  |
|  | **Step** | **Action** |  |
|  | 6a | Connection drops while the announce is in flight |  |
|  | 7a | Daemon logs the error and schedules a retry |  |
|  | 8a | The tracker never completed the registration, so the retry is handled as an unknown IP; the protocol is self-healing |  |
|  | **Error State:** Timeout expiry |  |  |
|  | 1b | The tracker's timeout for an IP expires without a ping arriving |  |
|  | 2b | Tracker flushes the IP and its package list from memory |  |
|  | 3b | The daemon's next contact is handled as an unknown IP (re-registration) |  |
| **Assumptions/ Comments** | Announce-time hashing is deliberately omitted: it would cost a full read of the cache for no security benefit, since integrity is verified end-to-end by the downloader. A stale or bit-rotted file costs one wasted transfer and a blacklist entry on the requester's side. Running `pkg clean` empties the seed source; the resulting empty re-announce deregisters the daemon until a new package appears — cleaning the cache stops you from seeding. Timeout and ping cadence are pinned by tracker protocol v0.2 at `TIMEOUT = 60s` and `PING_INTERVAL = 20s`; both are config-overridable and the only hard rule is that the cadence stays shorter than the timeout. |  |  |
 
---
 
| UC-06 | Serve Packages (upload) |  |  |
| :---- | ----- | :---- | ----- |
| **Description** | The daemon streams package bytes to a remote daemon that requests them, over plain HTTP (`peer-transfer-spec-v0.2.md`). This is the serving end of the same wire as UC-02's fetch loop, kept as a separate use case because it is a different code path with different failure modes and test obligations — the fuzzer targets this use case's HTTP surface. pkg is not involved anywhere here; this is daemon-to-daemon traffic. |  |  |
| **Actors** | Primary | Requesting Daemon (a remote peer's machine, running its own UC-02) |  |
|  | Secondary | pkg cache (read-only), Tracker (miss path only) |  |
| **Trigger** | An incoming `GET /pkg/packageName-version` from a remote daemon, on the port announced to the tracker as the serving port |  |  |
| **Precondition** | Daemon is running. Network connectivity is available. |  |  |
| **Postcondition** | The requesting daemon has received the package bytes (which it verifies on its own end) or a definitive error. |  |  |
| **Error States** | 1 | Malformed or hostile request |  |
|  | 2 | Requested package not held locally (e.g. `pkg clean` since the last announce) |  |
|  | 3 | Connection drops mid-stream |  |
| **Operational Flow** | **Step** | **Action** |  |
|  | 1 | Remote daemon sends `GET /pkg/packageName-version` |  |
|  | 2 | Daemon validates the request — this is untrusted network input from a remote machine. The path namespace is deliberately unlike the mirror facade's, so a seeding daemon is not a syntactically valid pkg mirror |  |
|  | 3 | Daemon opens the package in the pkg cache (read-only), obtaining a file handle and a size |  |
|  | 4 | Daemon responds `200` with an accurate `Content-Length` and streams from the open file, so the sending side holds no more than a copy buffer in memory. No hash is computed on this side; the requester verifies against its own repository database |  |
|  | 5 | Requester signals transfer complete |  |
| **Alternative Flow** | **Error State:** Malformed or hostile request |  |  |
|  | **Step** | **Action** |  |
|  | 2a | Request validation rejects the input — a bad path or an invalid name-version is `400`, a non-`GET` method is `405` |  |
|  | 3a | Error response sent to the requester, which treats every non-`200` alike and moves to its next peer |  |
|  | 4a | Daemon continues serving; garbage input must never crash it. The fuzz target is this surface end to end: arbitrary bytes in, never a panic. Request framing itself is the standard library's responsibility |  |
|  | **Error State:** Package not found |  |  |
|  | 3b | Cache lookup returns not found |  |
|  | 4b | Return `404` to the requester; the daemon never serves data it does not hold |  |
|  | 5b | Send a full re-announce to the tracker (UC-05): if one entry has drifted, others may have too |  |
|  | **Error State:** Connection drops mid-stream |  |  |
|  | 4c | Connection to the requester is lost while streaming |  |
|  | 5c | Abort the stream and log the error |  |
|  | 6c | Recovery belongs to the requester, whose retry loop (UC-02) simply asks another peer |  |
| **Assumptions/ Comments** | There is no daemon-owned store to poll; existence in the pkg cache plus the requester's end-to-end verification replaces the old "confirm the package is verified" step. The serving side has no write path at all — it opens cache files read-only and never buffers, so unlike the fetch side it needs no temporary directory. Response writes are deliberately left without a deadline: a large package over a slow uplink is legitimate traffic, and a wall-clock deadline cannot distinguish it from a stall. A peer that trickles bytes indefinitely is out of scope in the same way a slow mirror is. |  |  |
 
---
 
| UC-07 | Repository Metadata (pkg update) |  |  |
| :---- | ----- | :---- | ----- |
| **Description** | pkg also requests repository metadata (catalog, meta.conf) from its mirrors, most visibly during `pkg update`. The daemon does not serve, cache, or proxy metadata: it returns an HTTP error for any non-package-file path, and pkg fetches the metadata from its next mirror. The signed catalog is the root of the whole integrity model and must always come from a real mirror. |  |  |
| **Actors** | Primary | pkg (triggered by the user's `pkg update`, or implicitly before other operations) |  |
|  | Secondary | P2P Daemon, conventional mirror |  |
| **Trigger** | pkg requests any non-package-file path from the daemon |  |  |
| **Precondition** | Daemon is running and configured as pkg's first mirror. |  |  |
| **Postcondition** | pkg holds a current, signed repository catalog obtained from a conventional mirror. |  |  |
| **Error States** | 1 | Next mirror also fails (outside this project's scope) |  |
| **Operational Flow** | **Step** | **Action** |  |
|  | 1 | User runs `pkg update` (or pkg refreshes its catalog implicitly) |  |
|  | 2 | pkg requests the repository metadata from its first mirror, the daemon |  |
|  | 3 | Daemon recognises a non-package-file path and returns an HTTP error |  |
|  | 4 | pkg falls through to its next mirror, fetches the catalog there, and verifies the repository signature as it normally does |  |
| **Alternative Flow** | **Error State:** Next mirror also fails |  |  |
|  | **Step** | **Action** |  |
|  | 4a | pkg reports its ordinary repository error to the user; the daemon is not involved |  |
| **Assumptions/ Comments** | The integration smoke test must confirm empirically that pkg's catalog fetch falls through mirrors cleanly — the drop-in design leans on this behaviour. The configuration mechanism that orders the mirrors (daemon first, real mirror second) is settled by the s
ame smoke test. |  |  |
