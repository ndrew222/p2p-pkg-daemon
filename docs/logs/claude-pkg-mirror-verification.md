# Work log — §7 mirror-fallthrough verification against the FreeBSD host

Author: claude. Feature: HANDOFF §7.1–§7.5 empirical verification.

**Status: RUN. §7.1–§7.5 are all answered.** The owner granted permission and
the experiment was executed against `root@45.76.163.52` on 2026-08-08. The host
was returned to baseline afterwards (teardown verified — see the end).

**The headline is bad news: §7.1's load-bearing assumption is false as the
design relies on it.** pkg does fall through on a non-200, but only between
*mirrors of one repository*, never between *repositories*. jmj is currently
designed as a repository that 404s and expects pkg to go elsewhere. It will
not. See "Results" and then "What this costs the design", which needs an owner
ruling and is not resolved here.

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

## Aborted first run — what it did and did not establish

A first run was made against the host and stopped part-way. Its request log
(`/root/jmjprobe/requests.jsonl`, 9 entries) is worth keeping, because two
things in it are real and one apparent finding is not.

**Real: `mirror_type: "http"` works — mechanism (2) is viable.** pkg 2.7.5
fetched `/mirrorlist`, parsed it, and then fetched `/meta.conf`, `/data.pkg`
and `/packagesite.pkg` from the *first* URL in the list. So the daemon *can* be
placed ahead of a real mirror inside one repository, which is the ordering
§7.2 asks for. Whether it *falls back* to the second URL on a non-200 is still
untested — the run never reached a package file.

**Real: pkg issues conditional GETs.** `/packagesite.pkg` was requested with
`If-Modified-Since: Thu, 06 Aug 2026 18:39:36 GMT`. This is new: §7.3 asks
about `HEAD` and `Range`, and neither appeared, but nobody thought to ask about
`If-Modified-Since`. **The facade has no answer for it today** — it does not
track upstream modification times, and a facade that ignores the header and
always replies `200` merely wastes bandwidth, while one that replies `304`
wrongly would serve pkg a stale catalogue. Worth a spec question of its own.

**A real probe bug, which turned out not to be the segfault's cause.** This run
also ended with pkg dumping core (`/root/pkg.core`, 13 MB) during "Processing
entries". The first reading — recorded here because the reasoning is worth
keeping — blamed the probe: urllib raises a 304 as an `HTTPError`, so the 304
above fell into the generic error branch, which replied `304` *with* a body and
a `Content-Length`. A 304 must not carry a body (RFC 9110 §15.4.5); sending one
desynchronises the connection and the client reads the body as the head of the
next response. That is a genuine defect, it is fixed in the script below, and
it would have been a sufficient explanation.

**It was not the explanation.** See §7.2 in Results: the crash reproduces with
a mirror list naming *only* the real upstream mirror, where the probe serves
one short text document and nothing else and the 304 path is never reached.
That control isolates this bug out. The segfault is pkg 2.7.5's, not ours.

Kept rather than deleted because the lesson generalises: a plausible
self-attributed cause is not a control, and "our harness is probably at fault"
is as much a claim needing evidence as "the system under test is at fault".
`AGENTS.md`'s last trap — do not claim a fact about a system you have not
inspected — cuts both ways.

**Design flaw in that run's config.** It disabled both stock repositories:

```
FreeBSD-ports: { enabled: no }
FreeBSD-ports-kmods: { enabled: no }
jmjprobe: { url: "http://127.0.0.1:8081/mirrorlist", mirror_type: "http", ... }
```

With every real repository disabled there is no second repository to fall back
*to*, so this configuration cannot test mechanism (3) even in principle — and
mechanism (2) versus (3) is the whole point. It also leaves the host with no
working package manager if the probe is stopped, which is the foot-gun above
arriving in practice. Test mechanism (3) with the stock repos left enabled and
a `jmjprobe` block at higher `priority`.

## Results

All measured on pkg 2.7.5 / FreeBSD 15.1-RELEASE-p1, ABI `FreeBSD:15:amd64`.

### The harness is sound (stated first, because everything below depends on it)

The probe proxied the signed catalogue from `pkgmir.geo.freebsd.org` and pkg
accepted it as a genuine repository: `pkg update -f` processed **37,789
packages** with `signature_type: fingerprints` intact. A real `pkg install`
through it succeeded end to end (exit 0), including resolving and fetching a
dependency. **This is the first time jmj's facade model has been exercised
against real pkg, and the metadata half of it works.**

The control that matters for every claim below: with the *identical* repo
config, `mode=proxy` succeeds and the file lands. Only the probe's response
differs between the passing and failing runs.

### §7.1 — Does pkg fall through to the next mirror on a non-200?

**Yes between mirrors, no between repositories.** The distinction the recon
flagged is the whole answer.

`man pkg-repository`, §REPOSITORY MIRRORING, on HTTP mirror lists:

> Mirrors are tried in the order listed **until a download succeeds**.

That is exactly the semantic jmj needs — and it is scoped to *mirrors*. On
multiple repositories the same page says only that pkg will **search** them in
`PRIORITY` order. Search is solve-time selection, not fetch-time retry.

Measured, with `jmjprobe` at priority 100 and stock `FreeBSD-ports` at priority
0, both enabled, both catalogues at 37,789 rows, and `pkg rquery -r
FreeBSD-ports` confirming the target package present in the *other* repository:

| Probe response | Exit | Outcome |
|---|---|---|
| `404 Not Found` | 1 | `pkg: …: Not Found`. Nothing fetched. No retry against `FreeBSD-ports`. |
| `503 Service Unavailable` | 1 | `pkg: …: Service Unavailable`. Same. |
| Connection refused (probe **not running**) | 1 | `pkg: …: Connection refused`. Same. |

**The third row is the load-bearing control**: the probe process was killed
entirely, so no code of mine was in the fetch path, and pkg still did not fall
back to a healthy repository that demonstrably held the package. The finding is
about pkg's model, not about this harness.

(That control needs `pkg fetch -U`. Without it pkg tries to refresh the dead
repository's catalogue first and aborts with exit 3 before reaching the fetch —
a separate and also useful finding: **an unreachable facade breaks `pkg update`
outright**, it does not degrade.)

### §7.2 — How is mirror ordering configured?

Of the three candidate mechanisms, **none currently delivers daemon-first,
real-mirror-second**:

1. **`mirror_type: srv`** — works, but ordering comes from DNS SRV records.
   Expressing a loopback daemon needs real DNS; not reachable from
   `repos/*.conf` alone. Untested for want of a zone to edit.
2. **`mirror_type: http`** — the documented mechanism, and the one that would
   fit jmj exactly. **It segfaults pkg 2.7.5.** `pkg update` dies with
   `Segmentation fault (core dumped)`, or with `Sandboxed process … terminated
   abnormally by signal: 11` followed by `pkg: No signature found`, i.e. the
   crash is in the sandboxed signature-verification child.
3. **Two repositories with `priority`** — configures cleanly, and gives
   selection without fall-through (§7.1 above). Useless for this purpose.

On (2), the crash is **not** malformed input from this harness. The list was
`URL: ` + whitespace + one URL per line, which is precisely what
`man pkg-repository` specifies. And it reproduces with a list naming **only**
the real upstream mirror — the probe then serves one short text document and
nothing else, and pkg still segfaults. Whether this is a known FreeBSD bug was
not investigated; it is reproducible in three lines of config and worth a PR to
FreeBSD if anyone wants to file one.

### §7.3 — Does pkg issue `HEAD` or `Range`?

**No, neither.** Every request the probe logged — catalogue refresh, `pkg
fetch`, and a real `pkg install` — was a plain `GET`. Zero `Range` headers,
zero `HEAD`. User-agents `pkg/2.7.5` and `fetch libfetch/2.0`.

Scope this honestly: all observed transfers were small and none was
interrupted. Resume-after-interrupt was not tested, and `Range` could
plausibly appear there. Facade open questions 1 and 5 are answered for the
normal path only.

### §7.4 — What does pkg do with a 200 whose body fails its checksum?

Probe returned `200` with a correct `Content-Length` and garbage bytes:

```
pkg: cups-smb-backend-1.0_12 failed checksum from repository
```

Exit 1, nothing landed, **and no attempt against the other repository** — the
same no-fall-through result as §7.1. So a facade that serves wrong bytes is a
broken install, not a degraded one. pkg does detect it; it just has nowhere to
go afterwards.

### §7.5 — Cache layout after a real `pkg install`

**Consistent with §4.1, and the cache stays flat.** During a real install pkg
requested:

```
GET /All/Hashed/papersize-default-a4-0.0.20120302_1~49f94c8aa7.pkg
GET /All/Hashed/libpaper-1.1.28_1~599a5a67ab.pkg
```

— the same `All/Hashed/…~hash10` shape §4.1(a) measured from a `pkg fetch -o`
probe. The two observations agree, which is what §5.6 said nothing had yet
established.

`find /var/cache/pkg -type d` returns only `/var/cache/pkg` itself: **nothing
writes `All/` or `Hashed/` into the cache.** The resulting entry is the §4.1(b)
symlink pattern exactly —

```
papersize-default-a4-0.0.20120302_1.pkg -> papersize-default-a4-0.0.20120302_1~49f94c8aa7.pkg   (757 B target)
```

§5.6 and §7.5 are both closed.

## What this costs the design — needs an owner ruling

The mirror facade is specified to answer `404` when it cannot serve a package,
on the assumption that pkg then tries a real mirror. **That assumption is
false.** A `404` from jmj is the end of the install, not a fall-through.

The shape of the fix is visible in the harness: the probe worked precisely
*because* it proxied upstream itself rather than 404ing. That suggests the
facade must fall back to the real mirror **server-side** — becoming a
proxy-with-fallback rather than a mirror-that-declines.

That is an architecture change touching UC-02's every failure path, all of
UC-07, and the facade spec's status-code table, so **per ground rule 2 it is
reported and not decided here.** Three things the owner needs to weigh, none of
which I have resolved:

1. Does the facade proxy upstream on a peer miss, and if so does that make the
   daemon a mandatory single point of failure for all pkg traffic?
2. If it proxies, the "daemon writes only to its own temp buffer" constraint
   and the `pkgsize`/`cksum` verification story both need revisiting for bytes
   that never came from a peer at all.
3. Or: keep the 404, and accept that a peer miss fails the install — which is a
   very different product.

Note that the `404` behaviour is *correct and desirable* under mirror-list
semantics; it is only wrong under repository semantics. If `mirror_type: http`
were not crashing, the original design would work as written. That makes "is
the pkg segfault fixable / fixed upstream?" a genuine input to the decision
rather than a curiosity.

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

1. ~~**Permission to run the experiment.**~~ **Resolved** — raised with the
   owner, who granted it explicitly ("It is indeed a disposable box"). Worth
   recording that the permission had to be granted as a settings rule by the
   owner personally: an agent cannot grant itself the capability, and should
   not try to route around the refusal when it is blocked from doing so.

2. ~~**Which fallback mechanism §7.1 is actually asking about**~~ — **Resolved,
   and it was the right thing to have stopped on.** The answer is that pkg has
   both, they behave differently, and jmj depends on the one it does not get.
   Had I assumed either reading, the experiment would have produced a confident
   wrong answer: assuming mirror-level would have "confirmed" fall-through from
   the man page without noticing jmj cannot use it, and assuming
   repository-level would have reported a flat "pkg does not fall through" and
   missed that the behaviour is real, documented and merely out of reach.

3. **Whether the facade proxies upstream or keeps its `404`.** This is the
   architecture consequence in "What this costs the design". It is a spec-level
   change and I have not picked a side. **Raised; needs an owner ruling before
   §5.3 or any further facade work.**

4. **Where ADRs sit in the precedence order.** AGENTS.md ground rule 1 says
   every change must map to "a use-case step, the tracker protocol spec, or an
   ADR", and its hard-constraints list cites ADR-001 as settled ("no NAT
   traversal (ADR-001)"). But `docs/adr/` appears nowhere in AGENTS.md's
   numbered precedence list, and ADR-001's own status line reads *"Proposed
   (drafted 2026-07-07; awaiting vetting by Andrew and Elroy)"*. So a document
   that is formally un-vetted is already being enforced as a hard constraint,
   and nothing says whether an ADR outranks a use case or the reverse.
   **Raised; unanswered. Not invented around** — I added `docs/adr/` to the
   handoff's document map as *existing* without asserting a precedence rank.

## How to demo it

Reproduces both halves in about five minutes: the facade model *working*, then
§7.1 *failing*. Run from a checkout with the script below saved as `probe.py`.
`$H` is the FreeBSD host.

**Use mechanism (3), not (2).** Mechanism (3) adds a repository under a new
name and leaves the stock ones enabled, so the host keeps a working package
manager throughout and the demo is interruptible at any step. Mechanism (2)
shadows `FreeBSD-ports` *and* segfaults pkg 2.7.5 — it is the finding, not the
demo.

**This installs software on the host.** Steps 4 and 6 are real installs. The
teardown removes them.

```sh
H=root@45.76.163.52

# 1. Ship the probe and start it in proxy mode (a faithful mirror).
ssh $H 'mkdir -p /root/jmjprobe && echo proxy > /root/jmjprobe/mode'
ssh $H 'cat > /root/probe.py' < probe.py
ssh $H '(nohup python3 /root/probe.py >/root/jmjprobe/server.log 2>&1 &); sleep 2; cat /root/jmjprobe/server.log'

# 2. Add the probe as a higher-priority repository. Stock repos stay enabled.
ssh $H 'mkdir -p /usr/local/etc/pkg/repos && cat > /usr/local/etc/pkg/repos/jmjprobe.conf' <<'EOF'
jmjprobe: {
  url: "http://127.0.0.1:8081",
  mirror_type: "none",
  signature_type: "fingerprints",
  fingerprints: "/usr/share/keys/pkg",
  priority: 100,
  enabled: yes
}
EOF

# 3. THE FACADE MODEL WORKS. pkg accepts the proxy as a genuine signed
#    repository. Expect ~37,789 packages processed, signatures intact.
ssh $H 'pkg update -f'

# 4. Control: with mode=proxy an install through the facade succeeds, exit 0.
ssh $H 'pkg install -y -r jmjprobe indexinfo && echo "CONTROL: exit $?"'

# 5. THE FINDING. Flip to 404 and try a package that is not cached.
#    pkg does NOT fall through to FreeBSD-ports, which holds it and is healthy.
ssh $H 'echo 404 > /root/jmjprobe/mode'
ssh $H 'pkg clean -ay >/dev/null 2>&1; pkg install -y -r jmjprobe gettext-runtime; echo "RESULT: exit $?"'

# 6. Same shape for corrupt bytes: caught as a checksum failure, still no
#    fallback. (503 and connection-refused behave identically.)
ssh $H 'echo badbytes > /root/jmjprobe/mode'
ssh $H 'pkg clean -ay >/dev/null 2>&1; pkg install -y -r jmjprobe gettext-runtime; echo "RESULT: exit $?"'

# 7. What pkg actually asked for, in order, with headers.
ssh $H 'cat /root/jmjprobe/requests.jsonl' | python3 -m json.tool --json-lines

# 8. Teardown. Restores the stock-only baseline.
ssh $H 'pkg delete -y indexinfo gettext-runtime 2>/dev/null; rm -rf /usr/local/etc/pkg/repos /root/jmjprobe /root/probe.py /var/db/pkg/repos/jmjprobe; pkg update -f' 
```

Step 3 succeeding and step 5 failing is the whole result: the same repository,
the same config, the same pkg — the *only* difference is whether the facade
answered `200` or `404`, and a `404` ends the install rather than redirecting
it. Swap step 5's `echo 404` for `echo 503`, or kill the probe entirely to get
connection-refused, and the outcome is the same exit 1.

Pick step 5's package to be one that is genuinely absent — `pkg clean -ay`
empties the cache, but an already-*installed* package makes pkg a no-op and the
demo silently proves nothing. Verify with `pkg info gettext-runtime` first.

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
            if e.code == 304:
                # A 304 MUST NOT carry a body (RFC 9110 §15.4.5). Replying with
                # one desynchronises the connection: the client reads the body
                # as the head of the next response. urllib raises 304 as an
                # HTTPError, so without this branch it fell into the generic
                # error path below and did exactly that.
                self.send_response(304)
                self.end_headers()
                return
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
plus `rm -rf /var/db/pkg/repos/jmjprobe`, `pkg delete -y` the test packages,
then `pkg update -f` to restore the stock catalogue.

**Teardown was run and verified.** Afterwards: `/usr/local/etc/pkg/repos` does
not exist (its absence is the baseline marker §7 recon relied on),
`/var/db/pkg/repos/` holds only `FreeBSD-ports` and `FreeBSD-ports-kmods`,
`pkg -vv` lists only the three stock repositories, and the restored kmods
catalogue reports **239 packages** — matching the figure §5.2 recorded from the
untouched host, which is a decent check that the box is back where it started.
The two test packages (`papersize-default-a4`, `libpaper`) were deinstalled.

The pkg cache was deliberately **not** cleaned: it is read-only input to jmj,
the two new entries are ordinary cached packages, and wiping it would have
destroyed the artefacts §4.1(b) measured.
