package daemon

// The mirror facade: the HTTP surface pkg talks to (UC-02, UC-07).
//
// The daemon is pkg's first mirror. pkg makes ordinary mirror requests; the
// facade either returns verified package bytes or an HTTP error, and pkg's own
// mirror fall-through handles the rest. pkg is never modified.
//
// Contract: docs/mirror-facade-spec-v0.1.md. The path rule there is derived
// from a worked mirror URL; the status codes are an implementer's choice
// recorded in that document, because the use cases say only "an HTTP error".

import (
	"errors"
	"fmt"
	"log"
	"net/http"
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

// PackageHashes is the facade's read-only view of pkg's repository database:
// the expected hash for a name-version. Integrity comes solely from here --
// the tracker never verifies and peers are not trusted.
//
// ASSUMPTION (unratified, see spec open question 2): the returned string is a
// lowercase hex SHA-256, matching internal/peer/fetch.go.
//
// Kept separate from RepositoryDatabase in watcher.go, which serves the
// announce-time size check. Both are views of the same repo DB and should
// probably merge once a real reader exists -- neither has one yet.
type PackageHashes interface {
	ExpectedHash(nameVersion string) (hash string, found bool)
}

// Facade serves pkg. Peers resolves who holds a package (the tracker client);
// Hashes supplies the expected hash. A nil Hashes means the repository
// database is not wired up yet, and every package request answers 404.
//
// Blacklist is the daemon's local record of peers that have served corrupt
// bytes (UC-02 §11c). It lives on the facade rather than per request precisely
// so it outlasts one request: a peer that fails verification is skipped by
// every later fetch too. Zero value ready to use; use one Facade per daemon so
// there is one list. A Facade must not be copied once used.
type Facade struct {
	Peers     peer.PeerLister
	Hashes    PackageHashes
	Blacklist peer.Blacklist
}

// ServeHTTP implements http.Handler.
func (f *Facade) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// HEAD included, deliberately: the facade cannot answer one without
		// doing the whole fetch. See spec open question 1.
		httpError(w, http.StatusMethodNotAllowed, "only GET is served")
		return
	}

	nameVersion, ok := packageRequest(r.URL.Path)
	if !ok {
		// UC-07: metadata, catalog, directory listings, anything not
		// All/<name-version>.pkg. The signed catalog must come from a real
		// mirror; the daemon never serves or proxies it.
		httpError(w, http.StatusNotFound, "not a package path; this mirror serves package files only")
		return
	}
	if nameVersion == "" {
		httpError(w, http.StatusBadRequest, "malformed package name-version")
		return
	}

	f.servePackage(w, nameVersion)
}

// servePackage runs UC-02 steps 5-10 for one package.
//
// The peer list is fetched here rather than delegating to peer.Download
// because Download collapses "tracker returned nothing" and "every peer
// failed" into one ErrNoPeers, and the mirror surface has to tell those apart:
// the first is a 404 (this mirror holds nothing), the second a 502 (peers
// claimed it and failed to deliver). The loop itself is peer.FetchFirst, which
// is where blacklist skipping and marking live.
func (f *Facade) servePackage(w http.ResponseWriter, nameVersion string) {
	if f.Hashes == nil {
		log.Printf("facade: %q: no repository database wired up", nameVersion)
		httpError(w, http.StatusNotFound, "no expected hash for this package")
		return
	}
	expectedHash, found := f.Hashes.ExpectedHash(nameVersion)
	if !found {
		log.Printf("facade: %q: not in the repository database", nameVersion)
		httpError(w, http.StatusNotFound, "no expected hash for this package")
		return
	}

	addrs, err := f.Peers.Peers(nameVersion) // IWant(name-version)
	if err != nil {
		// Tracker unreachable, timed out, or unparseable -- UC-02 treats all
		// three identically.
		log.Printf("facade: %q: tracker: %v", nameVersion, err)
		httpError(w, http.StatusBadGateway, "tracker unreachable")
		return
	}
	if len(addrs) == 0 {
		log.Printf("facade: %q: no peers", nameVersion)
		httpError(w, http.StatusNotFound, "no peer holds this package")
		return
	}

	// Try peers in tracker order, skipping any already blacklisted. A peer
	// that times out or errors costs one attempt; one that returns bytes
	// failing verification is blacklisted on the way past (UC-02 §11c) so
	// later requests do not pay for it again.
	data, err := peer.FetchFirst(addrs, nameVersion, expectedHash, &f.Blacklist)
	if err != nil {
		// Non-empty peer list, no verified bytes: either every attempt failed
		// or everything on the list is already known corrupt. Both are an
		// upstream fault, not "this mirror does not have it".
		log.Printf("facade: %q: %d peers exhausted: %v", nameVersion, len(addrs), err)
		httpError(w, http.StatusBadGateway, "no peer could serve a verified copy")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		// pkg hung up mid-response. Nothing to recover; the status line is
		// already sent.
		log.Printf("facade: %q: write to pkg: %v", nameVersion, err)
		return
	}
	log.Printf("facade: served %q (%d bytes)", nameVersion, len(data))
}

// packageRequest classifies a request path.
//
// ok is false for a non-package path (UC-07 -- 404). ok is true with an empty
// nameVersion for a path that is shaped like a package request but whose file
// name is not a valid name-version (400). The two are distinguished so a
// malformed package request is not silently reported as "this is metadata".
func packageRequest(urlPath string) (nameVersion string, ok bool) {
	// path.Clean resolves . and .. and collapses slashes, so a traversal
	// attempt cannot smuggle "All" into position.
	cleaned := path.Clean("/" + strings.TrimPrefix(urlPath, "/"))

	dir, file := path.Split(cleaned)
	if path.Base(strings.TrimSuffix(dir, "/")) != packageDir {
		return "", false
	}
	if !strings.HasSuffix(file, packageFileExtension) {
		// e.g. All/ itself, or a stray non-package file under All/.
		return "", false
	}

	// Same name-version rule the cache watcher applies to cache filenames:
	// a final hyphen splitting a non-empty name from a digit-initial version.
	name, version := parsePackageName(file)
	if name == "" || version == "" {
		return "", true
	}
	return name + "-" + version, true
}

// httpError sends a status and a short plain-text body. pkg ignores the body
// and falls through to its next mirror on any non-200; the text is for a human
// running curl against the daemon.
func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintln(w, msg)
}

// ErrNoRepositoryDatabase reports that the facade has no repo DB wired up.
// Exported so a caller can assert the wiring at startup rather than
// discovering it as a stream of 404s.
var ErrNoRepositoryDatabase = errors.New("daemon: no repository database configured")

// ListenAndServe runs the facade on addr.
//
// NOT wired into Daemon.startHTTPServer yet, deliberately. The facade is
// pkg-facing and belongs on a loopback port; config.DaemonConfig.ListenAddr is
// the peer-facing address announced to the tracker as servingPort. UC-01 lists
// a single "listen port", so there is no config field for a second listener
// and inventing one is a spec decision, not an implementation detail. Once
// that field exists, wiring is one call. See the work log.
func (f *Facade) ListenAndServe(addr string) error {
	if err := f.Check(); err != nil {
		return err
	}
	log.Printf("facade: mirror listening on %s", addr)
	return http.ListenAndServe(addr, f)
}

// Check reports whether the facade is fully wired. A facade missing its peer
// lister cannot serve anything; one missing its hashes answers 404 to every
// package request.
func (f *Facade) Check() error {
	if f.Peers == nil {
		return errors.New("daemon: no peer lister configured")
	}
	if f.Hashes == nil {
		return ErrNoRepositoryDatabase
	}
	return nil
}
