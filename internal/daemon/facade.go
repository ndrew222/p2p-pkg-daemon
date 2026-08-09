package daemon

// The mirror facade: the HTTP surface pkg talks to (UC-02, UC-07).
//
// Contract — ADRs only; the pkg↔daemon wire has no spec file:
//
//	adr-003-facade-fetch-semantics.md  fetch semantics, status codes,
//	                                   verification placement, no cache
//	adr-004-facade-path-rule.md        the path rule, GET-only
//	adr-005-metadata-proxying.md       the non-package branch
//	adr-006-upstream-mirror-config.md  cfg.UpstreamURL
//	adr-007-repository-topology.md     jmj fronts exactly ONE repository
//
// docs/mirror-facade-spec-v0.1.md is DEPRECATED and was never binding — it was
// drafted by an implementing agent, not the spec owner, and ADR-003 overruled
// the model it was built on. There is no v0.2. Do not cite it as a contract.
//
// jmj is pkg's ONLY mirror, not its first, and everything below follows from
// that. Fall-through happens between mirrors WITHIN a repository and never
// between repositories; jmj is configured as a repository, so an HTTP error
// from this handler ENDS the install rather than redirecting it. Measured on
// pkg 2.7.5 — docs/logs/claude-pkg-mirror-verification.md §7.1.
//
// So a package request is answered from one of two sources, and an error
// reaches pkg only when both are gone:
//
//	peer path      Spool into temp_dir, verify the hash, then serve. The spool
//	               buys the ability to abandon a bad peer and try the next one
//	               without pkg ever seeing a failure. It is not what makes the
//	               bytes trustworthy — pkg re-verifies everything it is handed
//	               against the same signed catalogue (UC-02 §10).
//	upstream path  Stream straight through from cfg.UpstreamURL. No spool, no
//	               []byte, no cache. Upstream is terminal: there is no next
//	               source to abandon it for, so withholding bytes buys nothing
//	               and would cost temp space sized to the largest package in
//	               the repository (2.83 GiB) on every miss.
//
// The asymmetry is ADR-003's and it is the point. Do not make the two paths
// symmetrical in either direction.
//
// Everything that is NOT a package request is relayed from the same upstream
// (ADR-005, UC-07): streamed, uncached, unverified, unmodified, with pkg's
// If-Modified-Since forwarded and upstream's status — including 304 — relayed
// unchanged. Relaying is not vouching. The bytes originate at the real mirror,
// the facade asserts nothing about them, and pkg verifies the repository
// signature itself against its own fingerprints; that is why "the signed
// catalogue comes from a real mirror" survives while "the daemon never proxies
// metadata" does not. There is in any case nothing the facade COULD verify a
// catalogue against: the repository database carries no hash for one, and is
// itself the thing being updated. A 304 is never synthesised — the daemon
// tracks no upstream modification times, and a wrong guess serves pkg a stale
// catalogue.
//
// This makes the facade a general reverse proxy for non-package paths, and
// facade_addr's loopback enforcement (config.ValidateFields, which refuses to
// start otherwise) is what keeps that from being an open proxy for whoever
// finds the port. It was already load-bearing when only package bytes were
// relayed; ADR-005 widened the surface it protects. DO NOT RELAX IT.
//
// Status codes — ADR-003's rebuilt table. The deprecated spec's is retired:
//
//	200  peer bytes, hash-verified                        UC-02 §10
//	200  upstream bytes, streamed                         UC-02 §8f–10f
//	400  under All/ but the stem is not a name-version    ADR-004
//	404  ONLY: provably absent from the repository DB     ADR-003
//	405  anything but GET                                 ADR-004
//	502  ONLY: peers AND upstream both failed             UC-02 §9g–10g
//	···  a non-package path answers with upstream's own
//	     status, relayed (200 / 304 / 404 / …)            ADR-005
//
// Four conditions the old table answered separately — tracker unreachable,
// empty peer list, every holder blacklisted, every holder tried and failed —
// collapse into one: all four go to upstream, and none of them is visible to
// pkg. They stay distinguishable in the log, which is where they belong.
// **An empty peer list is not an error at all.** It is the common case, and
// the 404 this file used to return for it failed every first-of-its-kind
// install.
//
// There is no 500 in that table, and the omission is deliberate: a local spool
// failure (peer.ErrSpool, an unwritable temp_dir) is a peer-PATH failure like
// any other and goes to upstream, which does not touch temp_dir and can still
// serve the request. ADR-003's governing rule is that an error reaches pkg only
// when peers and upstream have both failed, and a broken temp_dir does not stop
// upstream. **This is a live disagreement, recorded at HANDOFF §4.8(a) and
// awaiting a ruling:** internal/peer's ErrSpool doc says the error is
// distinguished "so the facade can answer 5xx", which is the opposite reading,
// and §5.3's interim edit to this file did answer 500. The cost of the choice
// made here is that a daemon whose temp_dir is broken degrades silently into a
// plain proxy, which is why the log line for it is the loudest in the file.
//
// There is no facade cache and there must not be one. Three documents forbid a
// daemon-owned store (AGENTS.md's hard constraints, UC-02's assumptions,
// UC-06's), the daemon writes only to its own temp_dir, and the bytes are
// retained anyway: pkg writes what it is served into /var/cache/pkg, which is
// the directory the daemon seeds from, so a proxied package joins the swarm
// exactly as a peer-fetched one does.
//
// A successful repository-database lookup is NOT proof the upstream can serve
// that package (ADR-007). repo_db_dir is scanned for every catalogue on the
// host, but jmj fronts one repository, so Repositories will happily return a
// hash for a package cfg.UpstreamURL cannot fetch. pkg resolves such a package
// to the other repository and never asks us, so the path is unreachable in
// practice — but the two predicates are not the same one, and the upstream
// non-200 branch below exists rather than being asserted away.
//
// The path rule is ADR-004's, is measured against pkg 2.7.5, and is unaffected
// by any of the above. watcher.go, repodb.go and three test files depend on
// it. Do not "fix" it while you are in here.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/ndrew222/p2p-pkg-daemon/internal/peer"
)

// packageDir is the directory segment pkg serves package files from on a
// conventional mirror:
//
//	/stable/FreeBSD:15:amd64/latest/All/gopls-0.22.0_1.pkg
//	                                ^^^
//
// Everything before it is repo path (branch, ABI, repo name) and varies per
// mirror; the facade matches on the tail and ignores the prefix.
const packageDir = "All"

// hashedDir is the optional subdirectory pkg puts between All/ and the file.
// Measured against pkg 2.7.5: "pkg fetch indexinfo" requests
//
//	/…/All/Hashed/indexinfo-0.3.1_1~ae9dce33aa.pkg
//
// while the repository database's own path column agrees. The earlier rule
// required All to be the second-to-last segment, so every real fetch was
// classified as metadata and answered 404 -- the daemon was a no-op against a
// live repository.
const hashedDir = "Hashed"

// PackageHashes is the facade's read-only view of pkg's repository database:
// the expected hash for a name-version. Integrity comes solely from here --
// the tracker never verifies and peers are not trusted.
//
// ASSUMPTION (unratified, see spec open question 2): the returned string is a
// lowercase hex SHA-256, matching internal/peer/fetch.go.
//
// Declared separately from RepositoryDatabase in watcher.go, which serves the
// announce-time size check. Both are views of the same repo DB row, and one
// reader supplies both via the Repository composite in repository.go -- but the
// narrow interfaces stay, so each consumer's signature still states exactly
// what it may ask for. See HANDOFF.md §4.3.
type PackageHashes interface {
	ExpectedHash(nameVersion string) (hash string, found bool)
}

// Facade serves pkg. Peers resolves who holds a package (the tracker client);
// Repo supplies the expected hash and size from pkg's repository database; and
// UpstreamURL is the conventional mirror behind both fallback paths. A facade
// missing any of the three cannot honour the contract above, which is why
// Check rejects it and the daemon refuses to start rather than serving a
// silent stream of errors; the handler's own nil guards are for a Facade built
// directly, in tests.
//
// Blacklist is the daemon's local record of peers that have served corrupt
// bytes (UC-02 §11c). It lives on the facade rather than per request precisely
// so it outlasts one request: a peer that fails verification is skipped by
// every later fetch too. Zero value ready to use; use one Facade per daemon so
// there is one list. A Facade must not be copied once used.
type Facade struct {
	Peers     peer.PeerLister
	Repo      Repository
	Blacklist peer.Blacklist

	// TempDir is config.TempDir: where a peer download is spooled while it
	// is in flight (UC-02 §8). Empty means os.TempDir(). The file is created
	// per request and removed before the response returns -- the daemon has
	// no store and never serves from here. The upstream path does not use it
	// at all, and must not start.
	TempDir string

	// UpstreamURL is config.UpstreamURL (ADR-006): the base URL of a
	// conventional mirror, required, no default, with ${ABI} already
	// expanded by the time the daemon starts. Under ADR-005 it does not
	// merely name a fallback source -- it names the repository pkg actually
	// gets.
	UpstreamURL string
}

// upstreamClient is the one HTTP client the facade uses for UpstreamURL.
//
// It carries no Timeout, deliberately: http.Client.Timeout covers the body
// too, and a slow mirror is out of scope exactly as a slow peer is
// (AGENTS.md). The largest package in the repository is 2.83 GiB and a
// domestic uplink is legitimate traffic, so a wall-clock deadline here would
// fail transfers that are merely large. Cancellation comes from the pkg
// request's context instead, so pkg hanging up stops the upstream fetch.
//
// One shared client, so connections are reused: catalogue traffic is pure
// pass-through that the swarm never reduces, and reconnecting per request
// would add to the one cost ADR-005 asks us to keep small.
//
// Transparent compression is off. Left on, the transport adds its own
// Accept-Encoding, silently gunzips what comes back and drops Content-Length
// with it -- so the facade would relay bytes that are not the bytes upstream
// sent, which is precisely what ADR-005's "unmodified" forbids, and on the
// package path would produce a body that cannot match packages.cksum.
var upstreamClient = &http.Client{Transport: upstreamTransport()}

func upstreamTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DisableCompression = true
	return t
}

// ServeHTTP implements http.Handler.
func (f *Facade) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// HEAD included, deliberately: the facade cannot answer one without
		// doing the whole fetch, and pkg 2.7.5 never sends one -- measured
		// across a catalogue refresh, a pkg fetch and a real pkg install
		// (§7.3). ADR-004 keeps GET-only on that basis.
		httpError(w, http.StatusMethodNotAllowed, "only GET is served")
		return
	}

	nameVersion, ok := packageRequest(r.URL.Path)
	if !ok {
		// UC-07: meta.conf, packagesite.pkg, data.pkg, directory listings,
		// "/", anything not All/[Hashed/]<name-version>.pkg. Relayed from
		// upstream (ADR-005). This branch used to answer 404, which §7.1
		// measured breaks `pkg update` outright -- there is no next mirror
		// to fetch the catalogue from.
		f.relayUpstream(w, r)
		return
	}
	if nameVersion == "" {
		httpError(w, http.StatusBadRequest, "malformed package name-version")
		return
	}

	f.servePackage(w, r, nameVersion)
}

// expectedPackage is one row of pkg's repository database: the two values that
// bound and verify a transfer. They arrive together or not at all -- the peer
// transfer spec calls an implementation holding one without the other "a bug,
// not a case to handle gracefully" -- so they travel as one value.
type expectedPackage struct {
	hash string
	size int64
}

// servePackage runs UC-02 for one package: peers first, upstream second.
func (f *Facade) servePackage(w http.ResponseWriter, r *http.Request, nameVersion string) {
	want, ok := f.expected(nameVersion)
	if !ok {
		// The one surviving 404 (ADR-003): provably absent from pkg's own
		// repository database. Not "no peer has it" and not "upstream said
		// no" -- this is the request the facade can prove is unanswerable,
		// because without an expected hash and size there is nothing to
		// verify a peer against and nothing to bound a transfer by.
		log.Printf("facade: %q: not in the repository database", nameVersion)
		httpError(w, http.StatusNotFound, "not in this mirror's repository database")
		return
	}

	if f.servePeers(w, r, nameVersion, want) {
		return
	}
	f.serveUpstreamPackage(w, r, nameVersion, want)
}

// expected reads the hash and the exact size from the same repository-database
// row. Both or neither: a row carrying one and not the other is a defect in
// the reader, and is reported as one rather than half-honoured.
func (f *Facade) expected(nameVersion string) (expectedPackage, bool) {
	if f.Repo == nil {
		log.Printf("facade: %q: no repository database wired up", nameVersion)
		return expectedPackage{}, false
	}
	hash, hashOK := f.Repo.ExpectedHash(nameVersion)
	size, sizeOK := f.Repo.ExpectedFileSizeBytes(nameVersion)
	if hashOK != sizeOK {
		log.Printf("facade: %q: repository database has hash=%v size=%v; they come from one row and must agree",
			nameVersion, hashOK, sizeOK)
	}
	if !hashOK || !sizeOK {
		return expectedPackage{}, false
	}
	return expectedPackage{hash: hash, size: size}, true
}

// servePeers runs UC-02 steps 5-10 and reports whether pkg has been answered.
//
// false means "nothing has been written to w; try upstream". Every peer-side
// failure returns false: the tracker being unreachable, an empty peer list,
// every holder blacklisted, every holder tried and failed, and a temp_dir we
// cannot write. Under ADR-003 none of those is an answer to pkg -- they are
// reasons to go upstream, distinguished in the log and nowhere else.
func (f *Facade) servePeers(w http.ResponseWriter, r *http.Request, nameVersion string, want expectedPackage) bool {
	addrs, err := f.Peers.Peers(nameVersion) // IWant(name-version)
	if err != nil {
		// Tracker unreachable, timed out, or unparseable -- UC-02 treats all
		// three identically (§5a/6a).
		log.Printf("facade: %q: tracker: %v; going to upstream", nameVersion, err)
		return false
	}
	if len(addrs) == 0 {
		// UC-02 §6b/7b: the ordinary path for any package the swarm has not
		// seen yet. Deliberately not logged as a fault, because it is not
		// one -- it is what the upstream path exists to absorb.
		log.Printf("facade: %q: no peer holds it yet; going to upstream", nameVersion)
		return false
	}

	// Try peers in tracker order, skipping any already blacklisted. A peer
	// that times out, errors, or breaches the size bound costs one attempt;
	// one that returns bytes failing verification is blacklisted on the way
	// past (UC-02 §11c) so later requests do not pay for it again.
	// FetchFirst logs which peer served the bytes, so nothing here needs the
	// winning address.
	//
	// What comes back is an OPEN, REWOUND temp file whose contents already
	// match want exactly -- not a []byte. The spool is the fetch's (peer
	// transfer spec v0.2), the facade serves from the handle, and nothing
	// above the copy buffer is ever resident. That is what makes the 2.83 GiB
	// package servable on a 1 GiB host.
	//
	// The FILE IS OURS from here: peer.FetchFromPeer's contract is that the
	// caller closes and removes it, and peer.Discard is the one way to do
	// both. A spool that outlives its request is a cache nobody decided to
	// build.
	spool, err := peer.FetchFirst(r.Context(), addrs, nameVersion,
		peer.Want{Hash: want.hash, Size: want.size}, f.TempDir, &f.Blacklist)
	if err != nil {
		// Every failure here is a reason to go upstream. That includes
		// peer.ErrSpool -- an unwritable temp_dir -- which is this
		// daemon's fault rather than any peer's, but is still not a
		// reason to fail pkg: the upstream path does not touch temp_dir
		// and can serve the request. See the note on ErrSpool below.
		//
		// The two are named apart because they have opposite remedies:
		// nobody nearby holding the package needs no action at all, and a
		// broken temp_dir needs an operator. Under ADR-003 neither is
		// visible to pkg, so this log is the only place the difference
		// survives.
		if errors.Is(err, peer.ErrSpool) {
			log.Printf("facade: %q: PEER PATH DISABLED, this daemon cannot write temp_dir: %v; serving from upstream",
				nameVersion, err)
		} else {
			log.Printf("facade: %q: %d peer(s) tried, none served a verified copy: %v; going to upstream",
				nameVersion, len(addrs), err)
		}
		return false
	}
	// The buffer is per-request and ephemeral (UC-02 §8, §10): closed and
	// removed on every path, including the ones where pkg hangs up on us.
	defer peer.Discard(spool)

	// want.size, not a Stat: FetchFromPeer rejects any transfer whose length
	// disagrees with the repository database, so a file that got here is
	// exactly this long. Measuring it again would be asserting a guarantee
	// the wire already enforces.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(want.size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, spool); err != nil {
		// pkg hung up mid-response. The status line is already on the wire;
		// nothing to recover and nothing left to say.
		log.Printf("facade: %q: write to pkg: %v", nameVersion, err)
		return true
	}
	log.Printf("facade: served %q from a peer (%d bytes)", nameVersion, want.size)
	return true
}

// serveUpstreamPackage is UC-02 §8f-10f: fetch the package from the configured
// upstream mirror and stream it straight through to pkg.
//
// No spool and no []byte. Upstream is the terminal source, so there is nothing
// to withhold bytes for; integrity is pkg's, which re-verifies against the
// same signed catalogue (UC-02 §10). A mid-transfer failure gives pkg a short
// body against a promised Content-Length, which is exactly what it sees from a
// real mirror having a bad day and handles routinely (§11g).
func (f *Facade) serveUpstreamPackage(w http.ResponseWriter, r *http.Request, nameVersion string, want expectedPackage) {
	resp, err := f.fetchUpstream(r, r.URL.Path, "", false)
	if err != nil {
		log.Printf("facade: %q: upstream: %v", nameVersion, err)
		httpError(w, http.StatusBadGateway, "no peer and no upstream could supply this package")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Reachable in principle even though the repository database knows
		// this package: repo_db_dir holds every catalogue on the host and
		// upstream_url fronts one repository (ADR-007). Reported, not
		// assumed away.
		log.Printf("facade: %q: upstream answered %s", nameVersion, resp.Status)
		httpError(w, http.StatusBadGateway, "no peer and no upstream could supply this package")
		return
	}
	if resp.ContentLength >= 0 && resp.ContentLength != want.size {
		// pkgsize and the mirror disagree about the same file. Worth saying:
		// under ADR-007 it is one of the few signals that upstream_url is
		// serving a different repository from the catalogue we read.
		log.Printf("facade: %q: upstream Content-Length %d, repository database says %d",
			nameVersion, resp.ContentLength, want.size)
	}

	// The length promised to pkg is packages.pkgsize from the same row as the
	// hash (UC-02 §9f), not a count of bytes as they arrive: the facade
	// commits to a correct length rather than to whatever upstream produces.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(want.size, 10))
	w.WriteHeader(http.StatusOK)

	// Hashed incrementally for DIAGNOSTICS only (ADR-003). The bytes are not
	// withheld pending the verdict -- they are already on their way to pkg,
	// which re-verifies them itself.
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, sum), resp.Body)
	switch {
	case err != nil:
		log.Printf("facade: %q: upstream stream failed after %d of %d bytes: %v", nameVersion, n, want.size, err)
	case n != want.size:
		log.Printf("facade: %q: upstream sent %d bytes, expected %d", nameVersion, n, want.size)
	case hex.EncodeToString(sum.Sum(nil)) != want.hash:
		log.Printf("facade: %q: upstream bytes do not match the repository database hash; pkg will reject them", nameVersion)
	default:
		log.Printf("facade: served %q from upstream (%d bytes)", nameVersion, n)
	}
}

// relayUpstream is UC-07: fetch a non-package path from the configured
// upstream mirror and relay the answer to pkg.
//
// Streamed, uncached, unverified, unmodified (ADR-005). The status is
// upstream's own — a 404 for a catalogue file is upstream's answer and reaches
// pkg as such, because the facade has no standing to reinterpret it. The
// facade emits its own code in exactly one case: the fetch itself failed, and
// then it is ADR-003's 502, which is terminal. There is no next mirror, so
// `pkg update` fails outright — not a new cost, since §7.1 measured that an
// unreachable facade already breaks it.
//
// A 304 is relayed, never synthesised, and carries no body (RFC 9110 §15.4.5).
// It is not an optimisation: catalogue traffic is pure pass-through that the
// swarm never reduces, so the conditional GET is the only thing keeping the
// cost of being pkg's only mirror small.
func (f *Facade) relayUpstream(w http.ResponseWriter, r *http.Request) {
	resp, err := f.fetchUpstream(r, r.URL.Path, r.URL.RawQuery, true)
	if err != nil {
		log.Printf("facade: %s: upstream: %v", r.URL.Path, err)
		httpError(w, http.StatusBadGateway, "upstream mirror could not be reached")
		return
	}
	defer resp.Body.Close()

	relayHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if resp.StatusCode == http.StatusNotModified {
		log.Printf("facade: relayed 304 for %s", r.URL.Path)
		return
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		log.Printf("facade: %s: relay failed after %d bytes: %v", r.URL.Path, n, err)
		return
	}
	log.Printf("facade: relayed %s for %s (%d bytes)", resp.Status, r.URL.Path, n)
}

// hopByHopHeaders are the headers that belong to one connection and must not
// be forwarded across a relay (RFC 9110 §7.6.1). Not a design decision --
// ordinary HTTP correctness. Content-Length and Last-Modified are NOT here:
// pkg needs both, the second so that its next conditional GET has something to
// be conditional about.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// relayHeaders copies upstream's response headers to pkg, minus the ones that
// describe the upstream connection rather than the resource.
func relayHeaders(dst, src http.Header) {
	drop := make(map[string]bool, len(hopByHopHeaders))
	for _, h := range hopByHopHeaders {
		drop[http.CanonicalHeaderKey(h)] = true
	}
	// Connection names further headers that are hop-by-hop for this message.
	for _, value := range src.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				drop[http.CanonicalHeaderKey(name)] = true
			}
		}
	}
	for name, values := range src {
		if drop[http.CanonicalHeaderKey(name)] {
			continue
		}
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

// fetchUpstream issues the one GET the facade makes on pkg's behalf.
//
// The request context is pkg's, so a client that hangs up cancels the upstream
// fetch rather than leaving it running against the mirror.
//
// One request header crosses the boundary and only one: If-Modified-Since,
// which ADR-005 names. The package path does not even send that, because the
// facade must answer a package request identically whether the bytes come from
// a peer or from upstream and a peer cannot honour a conditional GET.
// Forwarding anything further is unspecified, so it is not done -- measured,
// pkg 2.7.5 sends no Range and no HEAD (§7.3), so there is nothing else the
// mirror needs to hear from pkg.
func (f *Facade) fetchUpstream(r *http.Request, reqPath, rawQuery string, conditional bool) (*http.Response, error) {
	target, err := upstreamURL(f.UpstreamURL, reqPath, rawQuery)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	if conditional {
		if ims := r.Header.Get("If-Modified-Since"); ims != "" {
			req.Header.Set("If-Modified-Since", ims)
		}
	}
	return upstreamClient.Do(req)
}

// upstreamURL joins a request path onto the upstream base URL.
//
// The path is CLIENT-SUPPLIED -- unlike a package request, which is reduced to
// a validated name-version before anything is fetched -- so this join is the
// facade's new attack surface (ADR-005). It must not permit escaping the base:
// no .. traversal, no absolute or scheme-relative path, nothing resolving above
// the repository root.
//
// The containment argument is one line: path.Clean("/" + p) always returns a
// rooted path with every "." and ".." resolved and every "//" collapsed, and a
// rooted path cannot climb past its own root -- "/.." cleans to "/". So the
// result is always base + something under base. The host, scheme and userinfo
// come from the configured base and are never read from the request, which is
// what makes an absolute-form request ("GET http://elsewhere/x") harmless: only
// its path is used. Percent-encoded traversal is covered too, because
// net/http has already decoded r.URL.Path by the time it reaches here.
func upstreamURL(base, reqPath, rawQuery string) (string, error) {
	if base == "" {
		return "", errors.New("upstream_url is not set (ADR-006: required, no default)")
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("upstream_url %q: %w", base, err)
	}
	if b.Scheme != "http" && b.Scheme != "https" {
		return "", fmt.Errorf("upstream_url %q: scheme must be http or https", base)
	}
	if b.Host == "" {
		return "", fmt.Errorf("upstream_url %q: no host", base)
	}

	// The base is cleaned too, so a stray "..", "." or double slash in the
	// configured value cannot widen what the join below can reach.
	basePath := strings.TrimSuffix(path.Clean("/"+strings.TrimPrefix(b.Path, "/")), "/")

	u := *b
	u.Path = basePath + path.Clean("/"+strings.TrimPrefix(reqPath, "/"))
	u.RawPath = "" // let url.String re-escape from Path
	u.RawQuery = rawQuery
	u.Fragment, u.RawFragment = "", ""
	return u.String(), nil
}

// packageRequest classifies a request path.
//
// ok is false for a non-package path (UC-07 -- relayed to upstream under
// ADR-005). ok is true with an empty nameVersion for a path that is shaped like
// a package request but whose file name is not a valid name-version (400). The
// two are distinguished so a malformed package request is not silently relayed
// as though it were metadata.
func packageRequest(urlPath string) (nameVersion string, ok bool) {
	// path.Clean resolves . and .. and collapses slashes, so a traversal
	// attempt cannot smuggle "All" into position.
	cleaned := path.Clean("/" + strings.TrimPrefix(urlPath, "/"))

	segments := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")

	// Find All anywhere in the path rather than at a fixed depth: the repo
	// path before it varies per mirror, per ABI and per branch, and pkg
	// puts a Hashed/ level after it. The LAST All wins, so a repository
	// that happens to be named "All" earlier in the path cannot displace
	// the real one.
	allAt := -1
	for i, seg := range segments {
		if seg == packageDir {
			allAt = i
		}
	}
	if allAt == -1 {
		return "", false
	}

	// What follows All/ is either the file, or Hashed/ and then the file.
	// Anything else -- a deeper tree, or All/ itself -- is not a package
	// request.
	rest := segments[allAt+1:]
	if len(rest) == 2 && rest[0] == hashedDir {
		rest = rest[1:]
	}
	if len(rest) != 1 {
		return "", false
	}

	file := rest[0]
	if !strings.HasSuffix(file, packageFileExtension) {
		// e.g. a stray non-package file under All/.
		return "", false
	}

	// Same name-version rule the cache watcher applies to cache filenames:
	// a final hyphen splitting a non-empty name from a digit-initial
	// version, after the ~hash10 suffix is stripped.
	name, version := parsePackageName(file)
	if name == "" || version == "" {
		return "", true
	}
	return name + "-" + version, true
}

// httpError sends a status and a short plain-text body.
//
// Note what a non-200 now means: there is no next mirror, so pkg reports a
// failed operation to the user rather than trying somewhere else (§7.1). The
// facade is entitled to send one only when it can prove it has nothing to
// serve. The text is for a human running curl against the daemon; pkg ignores
// the body.
func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintln(w, msg)
}

// ErrNoRepositoryDatabase reports that the facade has no repo DB wired up.
// Exported so a caller can assert the wiring at startup rather than
// discovering it as a stream of 404s.
var ErrNoRepositoryDatabase = errors.New("daemon: no repository database configured")

// ListenAndServe runs the facade on addr, which is config.FacadeAddr --
// loopback, enforced by config.ValidateFields.
//
// The daemon does not use this: Daemon.startHTTPServerLocked mounts the facade
// on its own http.Server so that shutdown is controllable, which
// http.ListenAndServe does not allow. This is the standalone entry point --
// convenient for a facade run on its own, and it keeps the Check-before-listen
// order in one obvious place.
func (f *Facade) ListenAndServe(addr string) error {
	if err := f.Check(); err != nil {
		return err
	}
	log.Printf("facade: mirror listening on %s", addr)
	return http.ListenAndServe(addr, f)
}

// Check reports whether the facade is fully wired. A facade missing its peer
// lister cannot reach the swarm; one missing its hashes cannot verify anything
// a peer sends; one missing its upstream has no fallback for a peer miss and
// no source for anything else.
func (f *Facade) Check() error {
	if f.Peers == nil {
		return errors.New("daemon: no peer lister configured")
	}
	if f.Repo == nil {
		return ErrNoRepositoryDatabase
	}
	if _, err := upstreamURL(f.UpstreamURL, "/", ""); err != nil {
		// ADR-006: required, no default, ${ABI} already expanded. config
		// validates the value; this catches a Facade wired without one.
		return fmt.Errorf("daemon: no usable upstream mirror: %w", err)
	}
	return nil
}
