package peer

// The seeding side of a peer transfer (UC-06). An ordinary HTTP server on the
// port this daemon announces to the tracker as servingPort.
//
// Contract: docs/peer-transfer-spec-v0.2.md (request surface, response codes,
// serving-side obligations, timeouts) and
// docs/adr/adr-002-serving-side-concurrency.md (the two caps and the 503).
//
// pkg is not involved anywhere here. This is daemon-to-daemon traffic, and the
// namespace is deliberately NOT the facade's .../All/<name-version>.pkg so that
// a seeding daemon cannot be mistaken for, or used as, a pkg mirror. Do not
// unify the two path rules.

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PackageSource is the read-only view of pkg's cache the serving side reads
// from. The daemon has NO store of its own -- it serves bytes straight out of
// the pkg cache, read-only.
//
// Open returns an OPEN HANDLE, not bytes, and that is the whole point of the
// interface. A []byte here is the regression that OOMs a 1 GiB host on the
// 2.83 GiB package: it puts the entire archive on the heap before a single
// byte reaches the socket. Handing back an io.ReadSeekCloser lets
// http.ServeContent stream it, and lets the runtime use sendfile where it can.
//
// size is the file's exact length in bytes. ok is false when this daemon does
// not hold the package -- the ordinary case after a `pkg clean`, answered 404
// and followed by a full re-announce (UC-06 §5b).
//
// The caller closes the handle. An implementation MUST NOT hash: the requester
// verifies end to end against pkg's repository database, and hashing here
// would be wasted I/O on bytes we are not trusted for anyway.
type PackageSource interface {
	Open(nameVersion string) (content io.ReadSeekCloser, size int64, ok bool)
}

// packagePathPrefix is the peer namespace. The path is EXACT: no repo-path
// prefix to ignore, no ".pkg" suffix, no Hashed/ level, and no metadata paths
// at all. `packagesite.pkg` and `meta.conf` have no representation on this
// wire.
const packagePathPrefix = "/pkg/"

// readHeaderTimeout bounds how long a peer may take to send its request
// headers. The response write is deliberately UNBOUNDED: a 2.83 GiB package
// over a domestic uplink is legitimate traffic and no wall-clock deadline can
// tell it apart from a stall. A slow peer is out of scope exactly as a slow
// mirror is -- do not add a stall detector, a minimum-throughput rule or a
// transfer deadline here.
const readHeaderTimeout = 10 * time.Second

// Server is the seeder (UC-06). The zero value plus a Source is usable, and
// Server itself is an http.Handler, so it can be mounted on a mux, driven by
// httptest, or given its own listener via ListenAndServe.
//
// It replaces a hand-written net.Listener accept loop that `continue`d on
// every Accept error including permanent ones, and hot-spun on a closed
// listener. net/http owns request framing now, which is also what makes the
// fuzz target meaningful: the project-owned surface under test is this
// handler.
type Server struct {
	// Source is the read-only pkg cache. A nil Source answers 404 to
	// everything rather than panicking, so a half-wired seeder degrades to
	// "holds nothing" instead of taking the daemon down.
	Source PackageSource

	// MaxConcurrentSeeds and MaxConcurrentSeedsPerIP are ADR-002's two
	// caps, from config.MaxConcurrentSeeds and
	// config.MaxConcurrentSeedsPerIP (HANDOFF §4.7). Both 0 means
	// unlimited, which is the default and changes nothing.
	MaxConcurrentSeeds      int
	MaxConcurrentSeedsPerIP int

	// OnNotHeld is the UC-06 §5b obligation: a serving daemon that
	// discovers it does not hold a package it advertised sends a FULL
	// re-announce, because if one entry has drifted others may have too.
	// Optional; nil disables it.
	//
	// It is called on 404 and on NOTHING ELSE. In particular a 503 must
	// never reach it: 404 means we have discovered we no longer hold
	// something we advertised, whereas 503 means we do hold it and are
	// refusing to serve it right now. Re-announcing on 503 would flood the
	// tracker precisely when the daemon is already at its limit (ADR-002).
	OnNotHeld func(nameVersion string)

	limiterOnce sync.Once
	limiter     *seedLimiter

	mu     sync.Mutex
	http   *http.Server
	closed bool
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// GET only. HEAD is answered because http.ServeContent gives it to us
	// for free; nothing depends on it and the requester never sends one.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		peerError(w, http.StatusMethodNotAllowed, "only GET is served on this wire")
		return
	}

	nameVersion, ok := packagePath(r.URL.Path)
	if !ok || !validName(nameVersion) {
		// 400, not 404. The Responses table in the peer spec assigns 400
		// to "path is not /pkg/<something>, or the name-version fails
		// validation", and 404 has to stay reserved for "not held",
		// because 404 -- and only 404 -- carries the UC-06 §5b
		// re-announce obligation. A malformed path is no evidence at all
		// about what this daemon holds, so answering 404 to one would
		// have a hostile peer driving our announce traffic.
		//
		// NOTE for the spec owner: the spec's *Request surface* section
		// says in passing that a non-exact path is a 404, which
		// contradicts its own Responses table. Implemented per the table
		// for the reason above; flagged in
		// docs/logs/claude-peer-wire-v0.2.md. The requester treats every
		// non-200 identically, so the distinction is for operators
		// reading logs, and flipping it is a one-line change.
		peerError(w, http.StatusBadRequest, "expected GET /pkg/<name-version>")
		return
	}

	// Admission control (ADR-002) sits AFTER the cheap rejections, so a
	// malformed request never consumes a slot, and BEFORE the source is
	// touched, so a refused request never opens a file descriptor -- the
	// descriptor budget is the resource the cap exists to protect, and it
	// is shared with the facade's outbound fetches and the keep-alive.
	release, refused, ok := s.limits().acquire(remoteIP(r))
	if !ok {
		// Diagnostics must name which cap fired and for which IP: an
		// attack and a misconfigured ceiling look identical in a bare
		// count and have opposite remedies (ADR-002).
		log.Printf("peer: 503 for %s: %s (in flight: %s); refusing immediately, no queueing", refused.ip, refused.reason, refused.inFlight)
		// No Retry-After, deliberately. The requester has other holders
		// to try and pkg's own mirror behind those, so inviting it to
		// wait converts a fast fall-through into a stall.
		peerError(w, http.StatusServiceUnavailable, "seeding capacity reached; try another peer")
		return
	}
	defer release()

	content, size, ok := s.open(nameVersion)
	if !ok {
		log.Printf("peer: 404 for %s: %q is not held", remoteIP(r), nameVersion)
		peerError(w, http.StatusNotFound, "not held")
		// UC-06 §5b. Called after the response so a slow tracker cannot
		// hold the requester's connection open, and in a goroutine for
		// the same reason -- the announce is the keep-alive's job, not
		// this request's.
		if s.OnNotHeld != nil {
			go s.OnNotHeld(nameVersion)
		}
		return
	}
	defer content.Close()

	// Content-Type is set explicitly so ServeContent does not sniff the
	// first 512 bytes of a package to guess it. ServeContent supplies an
	// accurate Content-Length, answers HEAD, and handles Range if a peer
	// asks -- all of which the spec permits and none of which the
	// requester relies on.
	//
	// An empty name and a zero modtime are deliberate: the name is only
	// used for content-type sniffing we have already pre-empted, and a
	// modtime would advertise cache-validation semantics this wire does
	// not have.
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, content)
	log.Printf("peer: served %q to %s (%d bytes, streamed from the cache)", nameVersion, remoteIP(r), size)
}

// open reads from the source, tolerating a nil one.
func (s *Server) open(nameVersion string) (io.ReadSeekCloser, int64, bool) {
	if s.Source == nil {
		return nil, 0, false
	}
	return s.Source.Open(nameVersion)
}

// limits builds the two semaphores once, from the configured caps.
func (s *Server) limits() *seedLimiter {
	s.limiterOnce.Do(func() {
		s.limiter = newSeedLimiter(s.MaxConcurrentSeeds, s.MaxConcurrentSeedsPerIP)
	})
	return s.limiter
}

// packagePath matches the peer namespace exactly: "/pkg/" followed by one
// segment. A deeper tree, a trailing slash, or anything outside /pkg/ is not a
// request on this wire.
func packagePath(urlPath string) (nameVersion string, ok bool) {
	rest, found := strings.CutPrefix(urlPath, packagePathPrefix)
	if !found || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

// remoteIP is the host half of r.RemoteAddr.
//
// NEVER a header. X-Forwarded-For and friends are client-supplied, and a cap
// keyed on client-supplied input is a cap the attacker sets. This matches the
// tracker, which does exactly this under an explicit spec rule
// (tracker-protocol-spec-v0.2.md: "the daemon's IP is always the connection's
// source address").
//
// A RemoteAddr with no port -- which net.Pipe and some test harnesses produce
// -- is used whole rather than discarded, so the cap still keys on something
// stable instead of silently collapsing every such peer into one bucket named
// "".
func remoteIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// peerError sends a status and a short text/plain body.
//
// There is no error MESSAGE type on this wire -- the status line is the error,
// and the requester ignores the body. The text exists for a human with curl.
func peerError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintln(w, msg)
}

// ListenAndServe runs the seeder on addr, which is config.ServingAddr. Unlike
// facade_addr this one is public by nature: peers are on other machines.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve runs the seeder on an already-open listener. It returns
// http.ErrServerClosed after Close, like any http.Server.
//
// Callers usually run this in a goroutine, so it has to cope with a Close that
// arrives BEFORE it: the closed flag is what makes that safe. Without it a
// shutdown raced against startup would leave the listener open with nothing
// serving it, which on the daemon's SIGHUP path meant the old serving_addr
// stayed bound and the new one could never take over.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return http.ErrServerClosed
	}
	if s.http == nil {
		s.http = &http.Server{
			Addr:    ln.Addr().String(),
			Handler: s,
			// Headers are bounded; the body write is not. See
			// readHeaderTimeout.
			ReadHeaderTimeout: readHeaderTimeout,
		}
	}
	srv := s.http
	s.mu.Unlock()

	log.Printf("peer: seed server listening on %s (max concurrent seeds: %s, per IP: %s)",
		ln.Addr(), capForLog(s.MaxConcurrentSeeds), capForLog(s.MaxConcurrentSeedsPerIP))
	return srv.Serve(ln)
}

// Close stops the seeder, and keeps it stopped: a Serve that starts afterwards
// closes its listener and returns rather than quietly resurrecting it.
//
// In-flight transfers are cut off, which is correct for a shutdown. The
// requester falls through to another holder, and nothing it received
// unverified can reach pkg.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	srv := s.http
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Close()
}

func capForLog(n int) string {
	if n <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", n)
}
