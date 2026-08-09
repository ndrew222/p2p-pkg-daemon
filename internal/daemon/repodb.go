package daemon

import (
	"database/sql"
	"errors"
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

	mu sync.RWMutex
	// rows is the package index; sources maps each catalogue's path to the
	// upstream URL pkg recorded for it. Both are swapped together by Reload.
	rows    map[string]repoRow
	sources map[string]string
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
// `pkg update`, so a long-running daemon's snapshot goes stale; RepoWatcher is
// the trigger (ADR-008), and SIGHUP calls it too when repo_db_dir moves.
//
// A FAILED RELOAD LEAVES THE PREVIOUS SNAPSHOT INTACT, and ADR-008 depends on
// that: at runtime the daemon has a working catalogue and the alternative to
// keeping a stale one is having none. Every error below returns before the
// swap, which is what makes that true; TestReloadFailureKeepsThePreviousSnapshot
// is the regression test.
func (r *Repositories) Reload() error {
	paths, err := repositoryDatabases(r.dir)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("daemon: no repository database found under %q (expected <repository>/%s)", r.dir, repoDBFile)
	}

	// The sources are read before anything is merged, because ADR-010 makes
	// them decide the merge order: our own catalogue is loaded first so that
	// the first-wins rule below resolves a collision in its favour without
	// needing a second rule. Advisory failures are swallowed by design -- a
	// source that cannot be read leaves its catalogue in path order, which is
	// where it was anyway. See upstreamcheck.go.
	sources := make(map[string]string)
	for _, path := range paths {
		if src, err := loadRepositorySource(path); err == nil && src != "" {
			sources[path] = src
		}
	}
	paths = ownCatalogueFirst(paths, sources)

	rows := make(map[string]repoRow)
	var conflicts []string
	for _, path := range paths {
		loaded, skipped, err := loadRepositoryDatabase(path)
		if err != nil {
			return fmt.Errorf("daemon: %s: %w", path, err)
		}
		// A row the daemon cannot trust is worse than useless: the fetch
		// path would compare peer bytes against it, never match, and
		// blacklist an honest peer for our own bad data. Dropping the row
		// means the package is simply not served, which is the correct
		// failure. Failing to start over it would be worse.
		//
		// The two causes are reported separately because they are
		// different faults with different diagnoses, and a single
		// "malformed cksum" message for a pkgsize problem sends the
		// reader looking in the wrong column.
		if n := len(skipped.badCksum); n > 0 {
			log.Printf("daemon: %s: skipped %d row(s) whose cksum is not a lowercase hex SHA-256: %s",
				path, n, namesForLog(skipped.badCksum))
		}
		if n := len(skipped.badPkgSize); n > 0 {
			log.Printf("daemon: %s: skipped %d row(s) whose pkgsize is not positive: %s",
				path, n, namesForLog(skipped.badPkgSize))
		}
		for nameVersion, row := range loaded {
			if existing, dup := rows[nameVersion]; dup {
				// First wins, and ownCatalogueFirst has already put OUR
				// catalogue first (ADR-010). That is not a tie-break: pkg
				// resolved the package from the jmj repository, so jmj's
				// row is the one pkg is acting on and the one the bytes it
				// re-verifies must match. Where no catalogue is ours, this
				// is still the ratified first-in-path-order rule.
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
				// Only DISAGREEING rows are reported. In the intended
				// deployment every row collides -- pkg writes jmj's own
				// catalogue into repo_db_dir, a copy of the repository we
				// front -- so logging every duplicate meant 37,813 lines
				// per reload, which teaches the reader to ignore the log
				// rather than telling them anything. A conflict is
				// different: it means the two catalogues have drifted,
				// which is the case where picking wrong makes us blacklist
				// an *honest* peer for our own bad data.
				if existing != row {
					conflicts = append(conflicts, nameVersion)
				}
				continue
			}
			rows[nameVersion] = row
		}
		log.Printf("daemon: loaded %d package(s) from %s", len(loaded), path)
	}
	if len(conflicts) > 0 {
		log.Printf("daemon: %d name-version(s) DISAGREE between repositories on hash or size; the catalogue this daemon fronts won, or the first in path order if none is ours. One of the catalogues is stale -- run pkg update: %s",
			len(conflicts), namesForLog(conflicts))
	}

	r.mu.Lock()
	r.rows = rows
	r.sources = sources
	r.mu.Unlock()
	return nil
}

// ownCatalogueFirst reorders paths so this daemon's own catalogue is merged
// first, which is how ADR-010 is implemented: the merge below already keeps the
// first row it sees, so putting ours in front resolves every collision in its
// favour without a second rule.
//
// "Ours" is a catalogue whose recorded source is a loopback URL. That is not a
// new idea here -- upstreamcheck.go already relies on it, in a comment written
// before this problem was found: once the operator has switched pkg over to
// jmj, pkg records OUR address as the repository's URL, because that is what
// pkg fetched from.
//
// Two properties matter and both are deliberate:
//
//   - With no loopback catalogue the order is UNCHANGED, so a host that has not
//     adopted jmj -- and the first start after switching, before pkg has
//     written our catalogue -- behaves exactly as before.
//   - The partition is stable, so several loopback catalogues (a degenerate
//     case; jmj fronts one repository per ADR-007) still resolve by path order
//     between themselves rather than by map iteration.
func ownCatalogueFirst(paths []string, sources map[string]string) []string {
	ours := make([]string, 0, 1)
	theirs := make([]string, 0, len(paths))
	for _, path := range paths {
		src, ok := sources[path]
		if !ok {
			theirs = append(theirs, path)
			continue
		}
		normalised, ok := normaliseRepoURL(src)
		if ok && isLoopbackURL(normalised) {
			ours = append(ours, path)
			continue
		}
		theirs = append(theirs, path)
	}
	if len(ours) == 0 {
		return paths
	}
	return append(ours, theirs...)
}

// Sources maps each catalogue's path to the upstream URL pkg recorded for it,
// as measured on the reference host:
//
//	repodata: packagesite | pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly
//
// Note the value is ALREADY EXPANDED -- pkg stores the resolved ABI, not
// ${ABI} -- which is what makes it usable for a direct comparison.
//
// Catalogues whose source could not be read are simply absent; this feeds a
// warning and must never be load-bearing.
func (r *Repositories) Sources() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.sources))
	for k, v := range r.sources {
		out[k] = v
	}
	return out
}

// loadRepositorySource reads the upstream URL pkg recorded for a catalogue.
//
// Separate from loadRepositoryDatabase because it is advisory and its failures
// are not: a catalogue with no repodata table, or no packagesite row, is not a
// broken catalogue. Returns "" rather than an error in that case.
func loadRepositorySource(path string) (string, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return "", err
	}
	defer db.Close()

	var url string
	err = db.QueryRow(`SELECT value FROM repodata WHERE key = 'packagesite'`).Scan(&url)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return url, nil
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

// skippedRows names the rows loadRepositoryDatabase dropped, split by cause.
// A row is attributed to the first cause it fails, so the two lists partition
// the dropped rows rather than double-counting one that is malformed twice.
type skippedRows struct {
	badCksum   []string
	badPkgSize []string
}

// loadRepositoryDatabase reads one catalogue into memory, returning the rows
// and the name-versions dropped, by cause.
func loadRepositoryDatabase(path string) (map[string]repoRow, skippedRows, error) {
	var skipped skippedRows

	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil, skipped, fmt.Errorf("opening: %w", err)
	}
	defer db.Close()

	// name and version are the clean columns; the ~hash10 suffix appears
	// only in path, so name-version here is already the key the facade and
	// the watcher produce via parsePackageName.
	rows, err := db.Query(`SELECT name, version, pkgsize, cksum FROM packages`)
	if err != nil {
		return nil, skipped, fmt.Errorf("querying packages: %w", err)
	}
	defer rows.Close()

	out := make(map[string]repoRow)
	for rows.Next() {
		var name, version, cksum string
		var pkgsize int64
		if err := rows.Scan(&name, &version, &pkgsize, &cksum); err != nil {
			return nil, skipped, fmt.Errorf("scanning packages: %w", err)
		}
		nameVersion := name + "-" + version
		switch {
		case !isHexSHA256(cksum):
			skipped.badCksum = append(skipped.badCksum, nameVersion)
		case pkgsize <= 0:
			skipped.badPkgSize = append(skipped.badPkgSize, nameVersion)
		default:
			out[nameVersion] = repoRow{hash: cksum, size: pkgsize}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, skipped, fmt.Errorf("reading packages: %w", err)
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
// logNameLimit entries with an "and N more" tail. It sorts a copy: rendering a
// diagnostic is not a reason to reorder the caller's slice, and a caller that
// later reported a count from the same slice would be reading a reordered one.
func namesForLog(keys []string) string {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	if len(sorted) <= logNameLimit {
		return strings.Join(sorted, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(sorted[:logNameLimit], ", "), len(sorted)-logNameLimit)
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
