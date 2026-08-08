# Work log — §7 mirror-fallthrough verification against the FreeBSD host

Author: claude. Feature: HANDOFF §7.1–§7.5 empirical verification.

**Status: harness designed and recon complete; the experiment itself is NOT
run.** It needs permission to start a process on the owner's host and to write
a pkg repository config there. See "Uncertainties" — this is raised, not
resolved.

## Why this and not §5.3

HANDOFF §5.3 (peer wire migration) is the nominal main work item, but the
handoff's own header says the unanswered §4.2 per-remote-IP cap blocks it, and
AGENTS.md ground rule 2 forbids picking an interpretation and continuing. §7.1
is unblocked, needs no ruling, and the handoff calls it "the single most
valuable thing left": whether pkg falls through to the next mirror on a non-200
is *the* load-bearing assumption of the whole design and has never been tested.

## Recon — what is now measured about the host

`root@45.76.163.52`, read-only commands only. Nothing was written.

| Fact | Value |
|---|---|
| OS | `FreeBSD 15.1-RELEASE-p1 amd64` (`vultr.guest`) |
| pkg | `2.7.5` — matches the version behind every §4.1 measurement |
| ABI | `FreeBSD:15:amd64`, osversion `1501000` |
| `/usr/local/etc/pkg/repos/` | **does not exist** |
| `/etc/pkg/FreeBSD.conf` | stock, unmodified |
| Tools present | `python3` 3.12.13, `sqlite3`, `fetch`, `nc`. **No `curl`.** |
| `/var/cache/pkg` | 32 entries; free space 20G |

Two findings that change the shape of §7.2, both new:

**(a) The host has never had a custom mirror configured at all.** There is no
`/usr/local/etc/pkg/repos/` directory. So there is no existing recipe to
inspect, confirm or copy — §7.2 has to be *derived*, not discovered, and
whatever is derived is the first such config this project has ever had.

**(b) The stock repository is `mirror_type: "srv"` over `pkg+https://`.**

```
FreeBSD-ports: {
  url: "pkg+https://pkg.FreeBSD.org/${ABI}/quarterly",
  mirror_type: "srv",
  signature_type: "fingerprints",
  ...
}
```

`_https._tcp.pkg.freebsd.org` resolves to a single SRV record
(`10 10 443 pkgmir.geo.freebsd.org`). This matters because **"next mirror" and
"next repository" are two different fallback mechanisms in pkg, and §7.1 does
not say which one it is asking about.** The handoff phrases §7.2 as "getting
the daemon *first* and a real mirror second", which presumes an ordered mirror
list within one repository. There are three candidate mechanisms and they have
materially different consequences for jmj:

1. **`mirror_type: "srv"`** — ordering comes from DNS SRV priority/weight.
   Requires the daemon to be in DNS. Not plausible for a loopback daemon.
2. **`mirror_type: "http"`** — pkg fetches the configured URL and expects a
   list of `URL: <base>` lines, then tries them in order. This *would* let the
   daemon hand pkg an ordered list naming itself first and a real mirror
   second. It is in no jmj spec.
3. **Two repositories with different `priority`.** This is repository
   fallback, not mirror fallback. Whether pkg retries a *different repository*
   for a package it already resolved from the higher-priority one is exactly
   the open question, and I expect the answer is no — repository selection
   happens at solve time, mirror selection at fetch time.

If (3) is the only mechanism that works and it does not retry, then a facade
404 is fatal to the install rather than a fall-through, and **the architecture
changes** — which is precisely the consequence §7.1 warns about. If (2) works,
jmj needs a mirror-list endpoint that appears in no spec today.

This distinction is the single most useful thing the recon produced, and it is
not recorded anywhere in `docs/`.

## Experiment design

A probe HTTP server stands in for the jmj facade. It proxies repository
metadata from a real mirror unchanged — so `signature_type: fingerprints`
stays valid and pkg will actually talk to it — and intercepts package-file
requests (`.../All/*.pkg`) according to a mode read from disk per request. It
appends every request to `requests.jsonl` with method, path and full headers.

That one harness answers five open questions in one sitting:

| Q | Mode | What it settles |
|---|---|---|
| §7.1 | `404`, `503` | Does pkg advance to the next mirror on a non-200, or abort? |
| §7.2 | — | Which of the three mechanisms above actually orders daemon-then-mirror |
| §7.3 | `proxy` | Whether pkg ever issues `HEAD` or `Range` (read straight off the log) |
| §7.4 | `badbytes` | 200, correct `Content-Length`, wrong bytes: next mirror or abort? |
| §7.5 | `proxy` | Cache layout after a real `pkg install` vs the `pkg fetch -o` probe |

§7.3 and §7.5 come for free from running the others — no extra steps.

The script is reproduced at the end of this log so the run is repeatable
without hunting for it. It was deliberately not added to the repo tree:
AGENTS.md fixes the layout and forbids new top-level directories, and a
throwaway probe is not a spec, a log or daemon code.

### Reversibility

Everything the experiment writes is confined to three places, all removable:
`/root/probe.py`, `/root/jmjprobe/`, and a new
`/usr/local/etc/pkg/repos/jmjprobe.conf`. `/usr/local/etc/pkg/repos/` does not
exist today, so teardown is `rm -rf` of a directory we created. The stock
`/etc/pkg/FreeBSD.conf` is not touched. §7.5 additionally needs one real
`pkg install` of a small package, which writes to `/var/cache/pkg` and installs
software — that one is not reversible by deleting a file, and is why the whole
thing is gated on asking.

## Difficulties

**Making pkg accept a fake mirror at all.** A repository with
`signature_type: "none"` is not a control — pkg's behaviour on an unsigned repo
is its own code path and would not tell us anything about the real one. Proxying
the signed metadata byte-for-byte from `pkgmir.geo.freebsd.org` and intercepting
only the package files keeps the signature path identical to production, which
is also exactly what the jmj facade does. This is the reason the probe is a
proxy rather than a static file server.

**`fetch(1)`, not `curl`.** The host has no curl. Not a problem, but any recipe
written for a future agent has to say `fetch -qo -`.

## Uncertainties

Per AGENTS.md ground rule 2, these were raised rather than resolved:

1. **Permission to run the experiment.** Standing up a listener on the owner's
   host, writing a pkg repo config, and doing one real `pkg install` are changes
   to a live machine. The handoff grants SSH access for §7 work, but that is not
   the same as standing authorisation to reconfigure the package manager.
   **Raised with the owner; unanswered. This is what blocks the log from having
   results in it.**

2. **Which fallback mechanism §7.1 is actually asking about** — mirror-level or
   repository-level (finding (b) above). The handoff and UC-07 both say "next
   mirror" without distinguishing. I did not pick one; the experiment is
   designed to test all three rather than to assume the answer. **Raised here,
   unanswered.**

3. **Where ADRs sit in the precedence order.** AGENTS.md ground rule 1 says
   every change must map to "a use-case step, the tracker protocol spec, or an
   ADR", and its hard-constraints list cites ADR-001 as settled ("no NAT
   traversal (ADR-001)"). But `docs/adr/` appears nowhere in AGENTS.md's
   numbered precedence list, and ADR-001's own status line reads *"Proposed
   (drafted 2026-07-07; awaiting vetting by Andrew and Elroy)"*. So a document
   that is formally un-vetted is already being enforced as a hard constraint,
   and nothing says whether an ADR outranks a use case or the reverse.
   **Raised; unanswered. Not invented around** — I added `docs/adr/` to the
   handoff's document map as *existing* without asserting a precedence rank.

## The probe script

`/root/probe.py`, run as `python3 /root/probe.py`, listening on 8081. Modes are
switched by writing one of `proxy` / `404` / `503` / `badbytes` into
`/root/jmjprobe/mode`; it is re-read per request, so no restart is needed
between cases.

```python
#!/usr/bin/env python3
"""Probe mirror for jmj §7 verification.

Stands in for the jmj mirror facade. Proxies repository metadata from a real
FreeBSD mirror so signatures stay valid, and intercepts package-file requests
(.../All/*.pkg) according to a mode read from disk at request time.
"""

import http.server
import json
import os
import socketserver
import time
import urllib.error
import urllib.request

BASE = "/root/jmjprobe"
MODE_FILE = os.path.join(BASE, "mode")
LOG_FILE = os.path.join(BASE, "requests.jsonl")
UPSTREAM = "https://pkgmir.geo.freebsd.org/FreeBSD:15:amd64/quarterly"
PORT = int(os.environ.get("PROBE_PORT", "8081"))
SELF_URL = os.environ.get("PROBE_SELF", "http://127.0.0.1:8081")


def mode():
    try:
        with open(MODE_FILE) as f:
            return f.read().strip()
    except FileNotFoundError:
        return "proxy"


def log(entry):
    entry["t"] = round(time.time(), 3)
    with open(LOG_FILE, "a") as f:
        f.write(json.dumps(entry) + "\n")
        f.flush()


def is_package(path):
    return "/All/" in path and path.endswith(".pkg")


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "jmjprobe/0"

    def _log_request(self, method):
        log({
            "method": method,
            "path": self.path,
            "mode": mode(),
            "is_pkg": is_package(self.path),
            "headers": dict(self.headers),
        })

    def log_message(self, fmt, *args):
        pass

    def do_GET(self):
        self._handle("GET")

    def do_HEAD(self):
        self._handle("HEAD")

    def _handle(self, method):
        self._log_request(method)
        m = mode()

        if self.path == "/mirrorlist":
            body = ("URL: %s\nURL: %s\n" % (SELF_URL, UPSTREAM)).encode()
            self._respond(200, body, "text/plain", method)
            return

        if not is_package(self.path):
            self._proxy(method)
            return

        if m == "404":
            self._respond(404, b"not found\n", "text/plain", method)
        elif m == "503":
            self._respond(503, b"busy\n", "text/plain", method)
        elif m == "badbytes":
            self._bad_bytes(method)
        else:
            self._proxy(method)

    def _bad_bytes(self, method):
        """A 200 whose body is the right length but the wrong content."""
        try:
            req = urllib.request.Request(UPSTREAM + self.path, method="HEAD")
            with urllib.request.urlopen(req, timeout=30) as r:
                size = int(r.headers.get("Content-Length", "1024"))
        except Exception:
            size = 1024
        self._respond(200, b"X" * size, "application/octet-stream", method)

    def _proxy(self, method):
        url = UPSTREAM + self.path
        headers = {}
        for h in ("Range", "If-Modified-Since", "User-Agent"):
            if h in self.headers:
                headers[h] = self.headers[h]
        try:
            req = urllib.request.Request(url, headers=headers, method=method)
            with urllib.request.urlopen(req, timeout=120) as r:
                body = r.read() if method == "GET" else b""
                ctype = r.headers.get("Content-Type", "application/octet-stream")
                log({"proxied": url, "upstream_status": r.status,
                     "upstream_len": r.headers.get("Content-Length")})
                self._respond(r.status, body, ctype, method,
                              extra={"Last-Modified": r.headers.get("Last-Modified")})
        except urllib.error.HTTPError as e:
            log({"proxied": url, "upstream_status": e.code})
            self._respond(e.code, b"upstream error\n", "text/plain", method)
        except Exception as e:  # noqa: BLE001
            log({"proxied": url, "error": str(e)})
            self._respond(502, b"probe upstream failure\n", "text/plain", method)

    def _respond(self, status, body, ctype, method, extra=None):
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        for k, v in (extra or {}).items():
            if v:
                self.send_header(k, v)
        self.end_headers()
        if method == "GET":
            try:
                self.wfile.write(body)
            except BrokenPipeError:
                pass


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


if __name__ == "__main__":
    os.makedirs(BASE, exist_ok=True)
    print("probe on %d, upstream %s" % (PORT, UPSTREAM), flush=True)
    Server(("0.0.0.0", PORT), Handler).serve_forever()
```

### Repo configs to test

**Footgun before you write either of these.** Repositories are keyed **by name**,
and `/usr/local/etc/pkg/repos/*.conf` is read *after* `/etc/pkg/FreeBSD.conf`.
A block named `FreeBSD-ports` therefore **replaces** the stock definition rather
than adding to it — it is the same mechanism the stock file's own header comment
uses to disable a repo. That is deliberate for mechanism (2) below, which needs
pkg to take its mirror list from the probe for that repository. But get the block
wrong and the host has no working ports repository until the file is removed.
Recovery is `rm` the file plus `pkg update -f`; nothing in `/etc/pkg` is touched
either way, which is what makes it recoverable.

Mechanism (3) uses a *new* name (`jmjprobe`), so it adds a repository and leaves
the stock one intact. That asymmetry between the two configs is the point of the
experiment, not an inconsistency.

Do not create `/usr/local/etc/pkg/repos/` speculatively and leave it empty. pkg
globs `*.conf` out of it, so an empty directory changes nothing — and its
*absence* is currently the evidence that this host is a clean baseline (see
recon finding (a)). Creating it early destroys that evidence and buys nothing.

Mechanism (2), `mirror_type: "http"` — the daemon serves the ordered list:

```
# /usr/local/etc/pkg/repos/jmjprobe.conf
FreeBSD-ports: {
  url: "http://127.0.0.1:8081/mirrorlist",
  mirror_type: "http",
  signature_type: "fingerprints",
  fingerprints: "/usr/share/keys/pkg",
  enabled: yes
}
```

Mechanism (3), repository priority — probe first, stock second:

```
jmjprobe: {
  url: "http://127.0.0.1:8081",
  mirror_type: "none",
  signature_type: "fingerprints",
  fingerprints: "/usr/share/keys/pkg",
  priority: 100,
  enabled: yes
}
```

Teardown: `rm -rf /usr/local/etc/pkg/repos /root/jmjprobe /root/probe.py`,
then `pkg update -f` to restore the stock catalogue.
