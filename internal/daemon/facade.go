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
	"io"
	"log"
	"net/http"
	"os"
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
// Repo supplies the expected hash and size from pkg's repository database. A
// nil Repo means the repository database is not wired up yet, and every package
// request answers 404.
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

	// TempDir is config.TempDir: where a download is spooled while it is
	// in flight (UC-02 §8). Empty means os.TempDir(). The file is created
	// per request and removed before the response returns -- the daemon has
	// no store and never serves from here.
	TempDir string
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
	if f.Repo == nil {
		log.Printf("facade: %q: no repository database wired up", nameVersion)
		httpError(w, http.StatusNotFound, "no expected hash for this package")
		return
	}
	expectedHash, found := f.Repo.ExpectedHash(nameVersion)
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
	// later requests do not pay for it again. FetchFirst logs which peer
	// served the bytes, so nothing here needs the winning address.
	data, err := peer.FetchFirst(addrs, nameVersion, expectedHash, &f.Blacklist)
	if err != nil {
		// Non-empty peer list, no verified bytes: either every attempt failed
		// or everything on the list is already known corrupt. Both are an
		// upstream fault, not "this mirror does not have it".
		log.Printf("facade: %q: %d peers exhausted: %v", nameVersion, len(addrs), err)
		httpError(w, http.StatusBadGateway, "no peer could serve a verified copy")
		return
	}

	// Spool the verified bytes through temp_dir before answering. UC-02 §8: a
	// download lands in the temp buffer, and only a complete, verified file
	// may reach pkg.
	//
	// Note what this costs today: the fetch has already materialised the whole
	// package in memory, so the spool is a disk round-trip that buys nothing
	// yet. It is here because the peer wire migration makes the fetch
	// streaming, at which point this file is what keeps a 900 MB package off
	// the heap. Delete it only together with that.
	if err := f.spool(w, nameVersion, data); err != nil {
		log.Printf("facade: %q: %v", nameVersion, err)
		httpError(w, http.StatusInternalServerError, "cannot buffer the download")
		return
	}
	log.Printf("facade: served %q (%d bytes)", nameVersion, len(data))
}

// spool writes verified bytes to a temp file and serves the file to pkg.
//
// It returns an error ONLY for failures that happen before anything has been
// written to w, so the caller can still send a status code. Once the response
// has begun, a failure is logged and nil is returned: the status line is
// already on the wire and there is nothing left to say.
func (f *Facade) spool(w http.ResponseWriter, nameVersion string, data []byte) error {
	tmp, err := os.CreateTemp(f.TempDir, "jmj-"+sanitiseForFilename(nameVersion)+"-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	// The buffer is per-request and ephemeral. Remove it on every path,
	// including the ones where pkg hangs up on us.
	defer func() {
		tmp.Close()
		if err := os.Remove(tmp.Name()); err != nil && !os.IsNotExist(err) {
			log.Printf("facade: %q: removing %s: %v", nameVersion, tmp.Name(), err)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewinding %s: %w", tmp.Name(), err)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, tmp); err != nil {
		// pkg hung up mid-response. Nothing to recover.
		log.Printf("facade: %q: write to pkg: %v", nameVersion, err)
	}
	return nil
}

// sanitiseForFilename keeps a temp file name readable for a human watching
// temp_dir without letting a name-version off the wire steer where the file
// lands. Everything outside the name-version alphabet becomes an underscore,
// so no separator, dot segment or NUL can survive into the path.
func sanitiseForFilename(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '.', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
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

// ListenAndServe runs the facade on addr, which is config.FacadeAddr --
// loopback, enforced by config.ValidateFields.
//
// The facade IS wired into Daemon.startHTTPServerLocked, which has mounted it
// at the root of facade_addr since §5.4. That path does not come through here:
// it needs its own *http.Server to shut down on reconfiguration, so it runs
// Check itself and serves the same handler. This entry point is for a caller
// that wants the facade and nothing else, and it has no other caller in the
// tree today.
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
	if f.Repo == nil {
		return ErrNoRepositoryDatabase
	}
	return nil
}
