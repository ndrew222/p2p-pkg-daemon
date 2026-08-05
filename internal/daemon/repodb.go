package daemon

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	// Pure Go, so `go build ./...` and `go test ./...` need no C toolchain
	// on any contributor's machine and cross-compiling to FreeBSD stays a
	// GOOS setting. A cgo driver would make a C compiler a prerequisite for
	// building the tracker too, which must run anywhere (AGENTS.md).
	_ "modernc.org/sqlite"
)

// repoDBFile is the SQLite file pkg writes inside each repository's directory:
// /var/db/pkg/repos/<repository>/db. Measured on FreeBSD 15.1-RELEASE-p1.
const repoDBFile = "db"

// cksumHexLen is the length of packages.cksum, a lowercase hex SHA-256.
const cksumHexLen = 64

// repoRow is one packages row, reduced to the two facts the daemon uses.
type repoRow struct {
	hash string
	size int64
}

// Repositories is a read-only snapshot of every repository database pkg has
// configured, indexed by name-version. It implements Repository.
//
// Snapshot rather than query-per-lookup, deliberately. Neither half of
// Repository returns an error, and that is only honest if a lookup cannot
// fail: a live query that hit an I/O error would have to report "not found",
// which the facade turns into a 404 telling pkg this mirror does not have the
// package when the truth is that this daemon is broken. Holding the rows costs
// about 6-12 MB for the ~38,000 packages measured on the reference host --
// roughly 1% of its RAM -- which is a fair price for a truthful signature.
//
// Safe for concurrent use. Reload swaps the snapshot under a write lock.
type Repositories struct {
	dir string

	mu   sync.RWMutex
	rows map[string]repoRow
}

// OpenRepositories loads every repository database under dir, which is
// config.RepoDBDir. Each repository is a subdirectory holding a file named
// "db"; the reference host has two, FreeBSD-ports and FreeBSD-ports-kmods.
//
// The databases are opened read-only and never written: they are pkg's signed
// catalogues and the hard constraints forbid touching them.
//
// It is an error for dir to contain no repository database at all. A daemon
// with an empty snapshot cannot verify a single package and would answer 404
// to everything, which is far better reported at startup than discovered as a
// silent stream of 404s.
func OpenRepositories(dir string) (*Repositories, error) {
	r := &Repositories{dir: dir}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// Reload rebuilds the snapshot from disk. pkg rewrites these files on
// `pkg update`, so a long-running daemon's snapshot goes stale; nothing calls
// this yet (the cache watcher watches the package cache, not the catalogues),
// and wiring a trigger is follow-up work.
func (r *Repositories) Reload() error {
	paths, err := repositoryDatabases(r.dir)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("daemon: no repository database found under %q (expected <repository>/%s)", r.dir, repoDBFile)
	}

	rows := make(map[string]repoRow)
	var collisions []string
	for _, path := range paths {
		loaded, skipped, err := loadRepositoryDatabase(path)
		if err != nil {
			return fmt.Errorf("daemon: %s: %w", path, err)
		}
		if skipped > 0 {
			// A row whose cksum is not a hex SHA-256 is worse than
			// useless: the fetch path would compare peer bytes against
			// it, never match, and blacklist an honest peer for our own
			// bad data. Dropping the row means the package is simply not
			// served, which is the correct failure.
			log.Printf("daemon: %s: skipped %d row(s) with a malformed cksum", path, skipped)
		}
		for nameVersion, row := range loaded {
			if _, dup := rows[nameVersion]; dup {
				// Ratified: the first repository in sorted path order
				// wins, deterministically, and the colliding names are
				// logged. Measured: zero collisions across both
				// repositories on the reference host (37,835 and 239
				// rows).
				//
				// The choice cannot be delegated to pkg even though pkg
				// has repository priority and has already picked a row
				// before it calls us: the facade needs an expected hash
				// *before* it fetches, and there is no "ask pkg" step.
				// Nor can the swarm disambiguate -- the tracker announces
				// a bare name-version and the peer namespace is
				// /pkg/<name-version> by design, so a peer holding a
				// colliding name-version cannot say which file it has.
				//
				// Picking wrong degrades to a failed install, never a
				// corrupt one, because UC-02 step 10 has pkg re-verify
				// the bytes we hand over. The one consequence that covers
				// is that a wrong row makes us blacklist an *honest* peer
				// for our own bad data -- which is why the names are
				// logged, and why first-wins beats refusing to start: the
				// downside is bounded and diagnosable, whereas refusing
				// lets one misconfigured third-party repository take the
				// daemon down.
				collisions = append(collisions, nameVersion)
				continue
			}
			rows[nameVersion] = row
		}
		log.Printf("daemon: loaded %d package(s) from %s", len(loaded), path)
	}
	if len(collisions) > 0 {
		log.Printf("daemon: %d name-version(s) appear in more than one repository; the first in path order won: %s",
			len(collisions), namesForLog(collisions))
	}

	r.mu.Lock()
	r.rows = rows
	r.mu.Unlock()
	return nil
}

// ExpectedHash implements PackageHashes: packages.cksum, the lowercase hex
// SHA-256 of the .pkg file. Verified byte-for-byte against cached files on the
// reference host, and 64-hex in all 38,074 rows across both repositories.
func (r *Repositories) ExpectedHash(nameVersion string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	row, ok := r.rows[nameVersion]
	return row.hash, ok
}

// ExpectedFileSizeBytes implements RepositoryDatabase: packages.pkgsize, the
// exact file size. Not flatsize, which is the installed size and 2-20x larger.
func (r *Repositories) ExpectedFileSizeBytes(nameVersion string) (int64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	row, ok := r.rows[nameVersion]
	return row.size, ok
}

// Len reports how many packages the snapshot holds.
func (r *Repositories) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.rows)
}

// repositoryDatabases lists <dir>/*/db in a deterministic order. A repository
// directory without a db file is skipped rather than reported: pkg creates the
// directory before it first fetches the catalogue, so an unfetched repository
// is a normal state, not an error.
func repositoryDatabases(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("daemon: reading repository database directory: %w", err)
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), repoDBFile)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// loadRepositoryDatabase reads one catalogue into memory, returning the rows
// and the number dropped for a malformed cksum.
func loadRepositoryDatabase(path string) (map[string]repoRow, int, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil, 0, fmt.Errorf("opening: %w", err)
	}
	defer db.Close()

	// name and version are the clean columns; the ~hash10 suffix appears
	// only in path, so name-version here is already the key the facade and
	// the watcher produce via parsePackageName.
	rows, err := db.Query(`SELECT name, version, pkgsize, cksum FROM packages`)
	if err != nil {
		return nil, 0, fmt.Errorf("querying packages: %w", err)
	}
	defer rows.Close()

	out := make(map[string]repoRow)
	var skipped int
	for rows.Next() {
		var name, version, cksum string
		var pkgsize int64
		if err := rows.Scan(&name, &version, &pkgsize, &cksum); err != nil {
			return nil, 0, fmt.Errorf("scanning packages: %w", err)
		}
		if !isHexSHA256(cksum) || pkgsize <= 0 {
			skipped++
			continue
		}
		out[name+"-"+version] = repoRow{hash: cksum, size: pkgsize}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("reading packages: %w", err)
	}
	return out, skipped, nil
}

// readOnlyDSN builds a SQLite URI that cannot write. The read-only constraint
// is enforced by the driver here rather than by our discipline elsewhere:
// mode=ro refuses to open for writing at all, and query_only rejects any
// statement that would mutate the file even if one were ever added.
func readOnlyDSN(path string) string {
	u := url.URL{
		Scheme:   "file",
		Path:     path,
		RawQuery: "mode=ro&_pragma=query_only(1)",
	}
	return u.String()
}

// logNameLimit bounds how many name-versions one diagnostic log line names
// before it summarises the rest. The lists it caps are unbounded in the bad
// case -- a misconfigured third-party repository can shadow ports wholesale,
// and a corrupt catalogue can drop every row it has -- and a log line naming
// 38,000 packages is not a diagnostic, it is a way of losing the rest of the
// log.
const logNameLimit = 10

// namesForLog renders keys as a sorted, comma-separated list of at most
// logNameLimit entries with an "and N more" tail. It sorts keys in place;
// every caller passes a slice it owns.
func namesForLog(keys []string) string {
	sort.Strings(keys)
	if len(keys) <= logNameLimit {
		return strings.Join(keys, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(keys[:logNameLimit], ", "), len(keys)-logNameLimit)
}

// isHexSHA256 reports whether s is exactly 64 lowercase hex digits.
func isHexSHA256(s string) bool {
	if len(s) != cksumHexLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
