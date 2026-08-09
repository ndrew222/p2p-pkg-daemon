# Work log — the FreeBSD host round

**Author:** Claude (Opus 5), 2026-08-09
**Host:** the project's reference box, FreeBSD 15.1-RELEASE-p1 / pkg 2.7.5 /
amd64, 1 CPU, 985 MiB RAM. Address held by the owner; deliberately not recorded
here, because this repository is public.
**Authorised by the owner this session**, for all four parts.

Everything below is measured. Where I am inferring rather than measuring, it
says so.

## Approach

The whole test suite and all three binaries **cross-compile from Linux** —
`GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0` — so the host needed no Go toolchain
and no source. `modernc.org/sqlite` is pure Go and ships `sqlite_freebsd_amd64.go`;
`fsnotify` has a kqueue backend. There are no `testdata` directories and no
`exec.Command` in any test, so `go test -c` binaries are self-contained.

```sh
GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go build -o out/ ./cmd/...
for p in config proto tracker discovery daemon peer; do
  GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go test -c -o out/$p.test ./internal/$p
done
```

Two measurement tools were written **outside the repository**, in a scratch
directory, and cross-compiled the same way. Neither is imported by anything in
`internal/`:

- **`evprobe`** — watches `repo_db_dir` with two independent fsnotify watch
  sets, one on directories and one on the `db` files, and prints every event
  with the file's inode. Written to settle the one platform question ADR-008
  could not answer by argument.
- **`dbdump`** — reads the catalogues read-only and reports `pkgsize` and
  `cksum`. Written because `pkg rquery "%sb"` reports *flatsize*, the installed
  size, which made every cached package look like a size mismatch. That wrong
  comparison is recorded because it is an easy trap: `%sb` is not the size of
  the package file.

## Results

### 1. The suite passes on FreeBSD

All six packages, run as cross-compiled binaries on the host: `config`,
`proto`, `tracker`, `discovery`, `daemon`, `peer` — `PASS` each. This is the
first time the cache watcher and the repository watcher have run against
**kqueue** rather than inotify, and the first time on the target OS at all.

`-race` was not run there: the race detector needs cgo and a FreeBSD C
toolchain, so it cannot be cross-compiled. It stays a Linux gate.

### 2. `pkg update` — what the filesystem actually does — **ADR-008 confirmed**

One `pkg update -f`, watched with `evprobe`:

| | |
|---|---|
| events under `repo_db_dir` | **21,650** |
| span | 3.0s → 25.4s |
| largest gap **inside** a catalogue rewrite | **0.48s** |
| one larger gap, 4.2s → 15.4s | **11.2s** — the download, before anything is written |

**The catalogue is replaced by a rename, and the file watch dies with it.** The
sequence is exact:

```
15.419s  DIR   CREATE   …/FreeBSD-ports/db-pkgtemp   ino=4953   (the OLD db, renamed aside)
15.419s  DIR   RENAME   …/FreeBSD-ports/db
15.420s  FILE  RENAME   …/FreeBSD-ports/db
15.420s  DIR   CREATE   …/FreeBSD-ports/db           ino=4952   (a NEW file, size 0)
…        DIR   WRITE    …                            (~21,600 more, over ten seconds)
```

The **FILE watch fired exactly twice in the entire run** — once per repository,
the `RENAME` — and then reported nothing ever again. Both `db` files got new
inodes (4953→4952 and 325806→321174) and every one of the ~21,600 subsequent
writes was invisible to it. The DIR watch saw all of them.

That is ADR-008's directory-watching decision confirmed on the target platform,
and it was not a marginal call: a file watch here goes permanently deaf on the
first update, silently. **The same shape reproduces on inotify**, checked
locally with a simulated rename, so this is not a kqueue quirk.

**The two-second settle is right, with margin.** The largest gap inside a
rewrite is 0.48s, so a 2s timer cannot fire mid-rewrite; the reload always reads
a complete catalogue.

**But there is one spurious reload per `pkg update`, and it is a new finding.**
pkg touches `lock` and `meta` *before* the download, then goes quiet for 11.2
seconds. The settle timer fires in that gap and reloads — correctly, and
completely — the **old** catalogue, which was already loaded. Then the rewrite
happens and a second, useful reload follows. Cost: one wasted 38,052-row reload
and one spurious re-announce per update. Not a correctness problem; the snapshot
is right at the end either way. Raised at HANDOFF §4.9 rather than fixed,
because narrowing which events arm the timer changes a mechanism ADR-008 states
("every event under `repo_db_dir` counts, whatever its op").

Live confirmation, separately: **six `pkg update -f` runs in quick succession
produced exactly one reload.** Coalescing works.

### 3. The real catalogues and the real cache

The daemon against the host's actual `/var/db/pkg/repos` and `/var/cache/pkg`:

```
daemon: loaded    239 package(s) from /var/db/pkg/repos/FreeBSD-ports-kmods/db
daemon: loaded 37,813 package(s) from /var/db/pkg/repos/FreeBSD-ports/db
Repository database: 38,052 packages
```

**§7's residual unknown is closed for this host.** `dbdump` checked every row of
both catalogues independently of the daemon: **38,052 rows, 0 with a `cksum`
that is not a lowercase-hex SHA-256, 0 with a non-positive `pkgsize`.** The
daemon logged no skipped rows either. The residual risk was always one *host*
rather than one repository, and this is one more host with zero exceptions.

**The announce list was 4 packages out of 20 cached, and the reason matters.**
All 20 cached name-versions are present in the catalogue. Sixteen have a
`pkgsize` that differs from the file on disk by a small amount, in both
directions:

```
name-version                       cache      pkgsize
ca_root_nss-3.125                 247539       247531   MISMATCH
expat-2.8.2                       133667       133686   MISMATCH
fish-4.6.0_2                     4842922      4842922   ok
indexinfo-0.3.1_1                   5905         5898   MISMATCH
python312-3.12.13_3             38257879     38254978   MISMATCH
…
4 ok, 16 size mismatch, 0 absent
```

Same name-version, different bytes: the repository was rebuilt without version
bumps. The catalogue in place before the round was dated 2026-08-06 and the one
after is dated 2026-08-08 22:41, while the cache was populated 2026-08-08 09:09.
**I cannot prove my `pkg update -f` caused the divergence** — I did not record
`pkgsize` before running it, and the newer catalogue would have arrived on the
next update regardless. What is measured is the end state and the daemon's
response to it.

The response is correct, and this is `SanityFilter` doing exactly the job it was
written for: a file whose size disagrees with the catalogue is not announced. If
it had been announced, a peer would have fetched it, failed the size bound and
wasted a transfer. (It would *not* have blacklisted us — a size breach is a
bound, not a verdict — which is the design working as intended two layers down.)

The operational consequence is worth stating plainly because nothing in the docs
anticipates it: **a host's shareable set decays as the repository is rebuilt,
even with no version changes at all.** Here it decayed to 20%.

### 4. End to end, with the real pkg

jmj configured as a repository — `mirror_type: "none"`, `url:
http://127.0.0.1:9001`, `priority: 100`, stock repositories left enabled, which
is ADR-007's coexistence — then:

- **`pkg update` through the facade: 37,813 packages processed**, `signature_type:
  fingerprints` intact. ADR-005's metadata relay, with a real pkg, on the signed
  catalogue. `facade: relayed 200 OK for /meta.conf (179 bytes)` and
  `/data.pkg (11,238,229 bytes)`.
- **`If-Modified-Since` / `304` relayed**, repeatedly, on later updates:
  `facade: relayed 304 for /meta.conf`, `… for /data.pkg`. The §6 defect closed
  in §5.7, confirmed live.
- **Peer hit.** `fetch http://127.0.0.1:9001/All/fish-4.6.0_2.pkg`:

  ```
  discovery: query pkg="fish-4.6.0_2" -> 1 peers
  peer: served "fish-4.6.0_2" to 127.0.0.1 (4842922 bytes, streamed from the cache)
  peer: fetched "fish-4.6.0_2" from 127.0.0.1:9002 (4842922 bytes, verified)
  facade: served "fish-4.6.0_2" from a peer (4842922 bytes)
  ```

  The SHA-256 of the bytes delivered begins `6f428aecbd`, which is exactly the
  `~hash10` suffix pkg gave the cached file — an independent check that the
  verification is against the right value.
- **Peer miss → upstream.** `xxd-9.2.0738` is cached but size-mismatched, so
  nobody announces it: `0 peers` → upstream → 200, and the bytes delivered were
  20,350, the *catalogue's* size, not the stale cached file's 20,321.
- **`404`** for a package absent from the catalogue, **`400`** for a malformed
  stem under `All/`. Both as ADR-003/ADR-004 specify.
- **A real install: `pkg install -y -r jmj tree` → exit 0**, `tree: 2.3.2 [jmj]`,
  fetched through the facade, verified by pkg, installed.
- **The loop closed on its own:** the cache watcher saw pkg write the new package
  and the announce went `packages=4` → `packages=5` within two seconds, with no
  restart and no prompting.
- **`temp_dir` was empty afterwards.** No spool survived a request.

**A finding that only appears in a real deployment:** configuring jmj as a pkg
repository creates `/var/db/pkg/repos/jmj/db` — *inside the directory jmj
reads*. The daemon then loads its own catalogue as a third repository and
reports 37,813 name-version collisions with `FreeBSD-ports`, resolving them
deterministically by path order, exactly as designed. Nothing broke, because
both catalogues came from the same upstream and agree. But the two can drift,
they are fetched at different times, and the loser's hash would then be used to
verify the winner's bytes. Raised at HANDOFF §4.10.

### 5. The `mirror_type: http` segfault — bug report now complete

All three items §7 listed as missing are done. The full, fileable report is
`docs/logs/freebsd-bug-report-pkg-mirror-type-http.md`.

- **The child core is captured**, via `sysctl kern.corefile='%N.%P.core'`. The
  parent's core is a signal re-raise from `main`, as predicted, and worthless.
- **The child's backtrace names the fault**: `fetchFreeURL` ← `libfetch_open` ←
  `pkg_fetch_file_to_fd` ← `pkg_repo_fetch_remote_tmp` ← `pkg_repo_fetch_meta`,
  with frame 0 inside libc's allocator. A free of a pointer that was never
  allocated, or freed twice, on the mirror-list path — crashing on the first
  fetch after the list is parsed.
- **Isolation run 1, `signature_type: none`: still crashes.** Not a signature
  bug. It gets further, fetching `meta.conf` and `data`, then reports
  `sqlite … pkgdb.c:2570: database is locked` and segfaults.
- **Isolation run 2, a stock web server** (`python3 -m http.server`): still
  crashes. The purpose-built probe from §7 is fully exonerated.
- **Control I added:** it reproduces with jmj not running at all.

I also controlled the `database is locked` message directly, because if jmj's
new watcher could make `pkg update` fail that would be a defect in what I had
just shipped: **six forced updates with the daemon running produced no lock
error and no failure.** The message did not recur. One observation is not a
finding, so it is reported in the bug report as possibly-related and claimed as
nothing more.

## Difficulties

**I compared against the wrong column and briefly had a defect that was not
there.** `pkg rquery "%sb"` returns flatsize, so all 20 cached packages looked
mismatched by 3–6×. Writing `dbdump` to read `pkgsize` from the catalogue
directly turned an alarming non-finding into the real, much smaller one. Worth
recording: the first version of that comparison would have made a confident and
completely wrong claim.

**A measurement artifact I nearly reported as a bug.** After six `pkg update -f`
runs I grepped the log for reloads and got zero, which looked like the watcher
failing on the real host. It was my grep racing the two-second settle: the
reload landed a moment later, and one reload for six updates is the coalescing
working. Checked before writing it down.

## Uncertainties

**Raised for the owner, both new, neither blocking:**

1. **§4.9** — one spurious reload per `pkg update`, from pkg touching `lock` and
   `meta` eleven seconds before it writes anything. Fixable by not arming the
   timer for those names; that contradicts ADR-008's "every event counts", so it
   is a ruling, not an edit.
2. **§4.10** — jmj's own repository catalogue lands inside `repo_db_dir` and
   collides with the upstream repository's on every row.

**Measured and closed:** ADR-008's platform assumption, §7's `cksum` unknown,
and all three missing pieces of the bug report.

**Not answered, and still needs a second machine:** where the tracker runs for a
real two-host trial. Everything here was one host talking to itself, which
exercises every code path but proves nothing about NAT, latency or a peer that
is not `127.0.0.1`.

## Host state on exit — verified, not assumed

Checked with read-only commands after teardown, not inferred from the teardown
commands succeeding:

| Check | Result |
|---|---|
| jmj / trac / http.server running | none |
| `/usr/local/etc/pkg/repos` | removed (it did not exist before) |
| `/var/db/pkg/repos` | `FreeBSD-ports`, `FreeBSD-ports-kmods` only |
| repositories pkg sees | stock three, `FreeBSD-base` disabled |
| installed packages | 18, `tree` removed |
| `/var/cache/pkg` | 40 entries, back to baseline, no `tree` |
| `kern.corefile` | restored to `%N.core` |
| `pkg update` / `pkg install -n` | both work |

**Left behind deliberately, and the owner may delete either:**

- `/root/cores/pkg.47469.core` and `pkg.47472.core` — the parent and child cores
  backing the bug report. Kept as evidence, in the same spirit as §7's
  `/root/pkg.core`, which is untouched.
- `/root/jmj/` — the cross-compiled binaries and `dbdump`/`evprobe`. Kept only
  because re-shipping them is a 54 MB upload; nothing runs from there.
