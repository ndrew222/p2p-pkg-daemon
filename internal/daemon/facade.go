package daemon

// The mirror facade: the HTTP surface pkg talks to (UC-02, UC-07).
//
// The daemon is pkg's only mirror. pkg makes ordinary mirror requests; the
// facade serves verified package bytes from a peer, or proxies to a configured
// upstream mirror when no peer can supply them. pkg is never modified.
//
// Contract: docs/adr/adr-003-facade-fetch-semantics.md for fetch semantics and
// status codes, docs/adr/adr-004-facade-path-rule.md for the path rule,
// docs/adr/adr-005-metadata-proxying.md for the non-package branch.
// docs/mirror-facade-spec-v0.1.md is DEPRECATED — do not treat it as the
// contract.
//
// ---------------------------------------------------------------------------
// SUPERSEDED (HANDOFF §5.7) — this file awaits a rewrite, not an edit.
//
// No longer blocked: the two owner rulings it was waiting on have landed
// (ADR-005, ADR-006). What follows still describes why it is wrong.
//
// It implements a model that has been measured false. On a peer miss it returns
// an HTTP error, assuming pkg falls through to its next mirror. It does not:
// fall-through happens between mirrors WITHIN a repository, never between
// repositories, and jmj is configured as a repository. A facade error ends the
// install. Measured in docs/logs/claude-pkg-mirror-verification.md §7.1.
//
// The tests pass. They encode the old contract, so passing tests are not
// evidence this file is correct — they are evidence it is consistently wrong.
//
// ADR-003 replaces the error path with a fetch from a configured upstream
// mirror, streamed through without spooling. ADR-005 (Approved) then settled
// the other branch: the facade PROXIES metadata, so the 404 this file returns
// for every non-package path is now a known defect rather than an open
// question -- it breaks `pkg update` outright (§7.1). The tests below that
// assert the refusal encode the retired rule and go with it.
//
// ADR-006 then settled where the upstream URL comes from: cfg.UpstreamURL,
// required, no default, with ${ABI} already expanded by the time the daemon
// starts. So the rework has everything it needs and is unblocked.
//
// Not blocked on, and not blocking, §5.3 (the peer wire) — different surface.
//
// The path rule below is UNAFFECTED and correct: measured, owner-ratified, and
// specified in adr-004-facade-path-rule.md. Do not "fix" it while here.
// ---------------------------------------------------------------------------

import (
	"errors"
	"fmt"
	"io"
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
// nil Repo answers 404 to every package request, which is why Check rejects it
// and the daemon refuses to start rather than serving a silent stream of 404s;
// the handler's own nil guard is for a Facade built directly, in tests.
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

	f.servePackage(w, r, nameVersion)
}

// servePackage runs UC-02 steps 5-10 for one package.
//
// The peer list is fetched here rather than delegating to peer.Download
// because Download collapses "tracker returned nothing" and "every peer
// failed" into one ErrNoPeers, and the mirror surface has to tell those apart:
// the first is a 404 (this mirror holds nothing), the second a 502 (peers
// claimed it and failed to deliver). The loop itself is peer.FetchFirst, which
// is where blacklist skipping and marking live.
func (f *Facade) servePackage(w http.ResponseWriter, r *http.Request, nameVersion string) {
	if f.Repo == nil {
		log.Printf("facade: %q: no repository database wired up", nameVersion)
		httpError(w, http.StatusNotFound, "no expected hash for this package")
		return
	}
	expectedHash, found := f.Repo.ExpectedHash(nameVersion)
	expectedSize, sizeFound := f.Repo.ExpectedFileSizeBytes(nameVersion)
	if !found || !sizeFound {
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
	// SEAM WITH §5.3 (peer wire v0.2). FetchFirst now spools to temp_dir
	// itself and hands back an open, rewound, already-verified file, so the
	// facade's own spool step is gone: the fetch's temp file IS the buffer
	// UC-02 §8 asks for. Nothing above the copy buffer is resident at any
	// point, which is what makes the 2.83 GiB package servable at all.
	//
	// This block is a mechanical adaptation to the new signature, not a
	// design change. The file is being rewritten wholesale under §5.7
	// (ADR-003/005/006) and everything around it still implements the
	// measured-false fall-through model.
	want := peer.Want{Hash: expectedHash, Size: expectedSize}
	tmp, err := peer.FetchFirst(r.Context(), addrs, nameVersion, want, f.TempDir, &f.Blacklist)
	if err != nil {
		// A spool failure is OURS -- an unwritable temp_dir -- not a
		// missing package. pkg must see a 5xx so it does not conclude
		// the file does not exist.
		if errors.Is(err, peer.ErrSpool) {
			log.Printf("facade: %q: %v", nameVersion, err)
			httpError(w, http.StatusInternalServerError, "cannot buffer the download")
			return
		}
		// Non-empty peer list, no verified bytes: either every attempt failed
		// or everything on the list is already known corrupt. Both are an
		// upstream fault, not "this mirror does not have it".
		log.Printf("facade: %q: %d peers exhausted: %v", nameVersion, len(addrs), err)
		httpError(w, http.StatusBadGateway, "no peer could serve a verified copy")
		return
	}
	// The buffer is per-request and ephemeral: remove it on every path,
	// including the ones where pkg hangs up on us.
	defer peer.Discard(tmp)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(expectedSize, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, tmp); err != nil {
		// pkg hung up mid-response. The status line is already on the
		// wire and there is nothing left to say.
		log.Printf("facade: %q: write to pkg: %v", nameVersion, err)
		return
	}
	log.Printf("facade: served %q (%d bytes)", nameVersion, expectedSize)
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
