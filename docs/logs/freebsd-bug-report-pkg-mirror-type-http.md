# FreeBSD bug report (ready to file): pkg 2.7.5 segfaults in `fetchFreeURL` with `mirror_type: http`

**Status:** complete and fileable. Everything HANDOFF §7 listed as missing is
now here — the child core's symbolised backtrace, and both isolation runs.
Measured 2026-08-09 on the project's reference host.

Suggested product/component: **Ports & Packages → Package Infrastructure**
(`pkg`). Severity: crash, no data loss, easy workaround (do not use
`mirror_type: http`).

---

## Summary

`pkg update` segfaults for any repository configured with `mirror_type: "http"`.
The crash is in `fetchFreeURL`, called from `libfetch_open`, while fetching the
repository's `meta.conf` — that is, immediately after the HTTP mirror list has
been fetched and parsed. It is 100% reproducible in three lines of config
against a stock web server.

## Environment

| | |
|---|---|
| OS | FreeBSD 15.1-RELEASE-p1 amd64 (GENERIC) |
| pkg | 2.7.5 (`/usr/local/sbin/pkg`; libpkg statically linked) |
| ABI | `FreeBSD:15:amd64` |
| Repository | stock `FreeBSD-ports`, `pkg+https://pkg.FreeBSD.org/${ABI}/quarterly` |

## Reproduction

Serve a one-line mirror list from any stock web server:

```sh
mkdir -p /root/mirrorlist
printf 'URL: https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly\n' > /root/mirrorlist/mirrorlist
cd /root/mirrorlist && python3 -m http.server 8082 --bind 127.0.0.1 &
```

Add a repository that reads it:

```
# /usr/local/etc/pkg/repos/crash.conf
crash: {
  url: "http://127.0.0.1:8082/mirrorlist",
  mirror_type: "http",
  signature_type: "fingerprints",
  fingerprints: "/usr/share/keys/pkg",
  priority: 50,
  enabled: yes
}
```

Then:

```
# pkg update -r crash
Updating crash repository catalogue...
Segmentation fault (core dumped)
```

The mirror-list document is exactly the format `pkg-repository(8)` specifies:
the literal `URL:`, whitespace, one URL per line.

**Two processes dump core**, a parent and a child. The parent's core is only a
signal re-raise from `main` and has no diagnostic value; the child's carries the
fault. Capture both with:

```sh
sysctl kern.corefile='/root/cores/%N.%P.core'
```

## Backtrace (the child core)

```
* thread #1, name = 'pkg', stop reason = signal SIGSEGV
  * frame #0: libc.so.7`___lldb_unnamed_symbol5761 + 134
    frame #1: pkg`fetchFreeURL + 21
    frame #2: pkg`libfetch_open + 1580
    frame #3: pkg`pkg_fetch_file_to_fd + 1118
    frame #4: pkg`pkg_repo_fetch_remote_tmp + 363
    frame #5: pkg`pkg_repo_fetch_meta + 124
    frame #6: pkg`pkg_repo_binary_update + 499
    frame #7: pkg`pkg_update + 15
    frame #8: pkgcli_update + 358
    frame #9: pkg`exec_update + 581
    frame #10: pkg`main + 2847
    frame #11: libc.so.7`__libc_start1 + 303
```

Frame 0 is inside libc's allocator, reached from `fetchFreeURL`, so this reads
as a free of a pointer that was never allocated — or a second free of one
already released — on `libfetch_open`'s mirror-list path. `libfetch_open` is
reached from `pkg_repo_fetch_meta`, i.e. the *first* fetch after the mirror list
is resolved.

**Symbolise against `/usr/local/sbin/pkg`, not `/usr/sbin/pkg`.** The latter is
the base-system bootstrap stub and yields only unnamed symbols.

## Isolation runs

Both of the controls this report needed have been done, and neither changes the
outcome.

**1. `signature_type: "none"` — still crashes.** The crash is not in signature
verification. With signatures off, pkg gets further: it fetches `meta.conf` and
`data` successfully and begins building the catalogue, then reports

```
pkg: sqlite error while executing CREATE TABLE packages (…) in file pkgdb.c:2570: database is locked
Segmentation fault (core dumped)
```

The `database is locked` message did not appear in every run and is not
required for the crash — a later repeat with a clean state segfaulted without
it. It is reported here because it may be the same underlying fault surfacing
in a second place.

**2. A stock web server — still crashes.** The mirror list is served by
`python3 -m http.server` with no application logic whatsoever, ruling out the
purpose-built probe used in earlier testing. This matters because an earlier
investigation of this crash briefly attributed it to a defect in that probe
(which was real, and separately fixed); it was not the cause, and this run
removes the probe from the picture entirely.

**3. Independent of any third-party daemon.** The crash reproduces with nothing
else running against the host.

## Impact

`mirror_type: "http"` is documented in `pkg-repository(8)` and appears to be
unusable in 2.7.5. Anyone trying to place a local caching or proxying mirror
ahead of the upstream mirrors within a single repository — the mechanism that
option exists for — hits this immediately.

## Workaround

Use `mirror_type: "srv"` (needs DNS control) or `mirror_type: "none"` with the
proxy as the repository's sole `url`. The `none` form is fully working: a
repository so configured updates its catalogue with signatures intact and
installs packages normally.

---

### Provenance

Measured for the p2p-pkg-daemon project, which needs `mirror_type: http` to
place a local daemon ahead of the upstream mirrors. Earlier evidence and the
first (inconclusive) run are in `docs/logs/claude-pkg-mirror-verification.md`
§7.2; the run that produced the backtrace above is in
`docs/logs/claude-freebsd-host-round.md`. Cores are retained on the reference
host at `/root/cores/` — `pkg.47469.core` (parent) and `pkg.47472.core` (child).
