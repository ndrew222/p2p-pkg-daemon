package daemon

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// The whole point of the merge: one reader satisfies both views.
var _ Repository = (*Repositories)(nil)

type fixtureRow struct {
	name    string
	version string
	pkgsize int64
	cksum   string
}

func hash64(c byte) string { return strings.Repeat(string(c), 64) }

// writeRepoDB creates <dir>/<repo>/db with the subset of pkg's schema the
// reader depends on. The extra columns are present because the real table has
// them and the query must not be sensitive to column order.
func writeRepoDB(t *testing.T, dir, repo string, rows []fixtureRow) string {
	t.Helper()
	repoDir := filepath.Join(dir, repo)
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repoDir, repoDBFile)

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE packages (
		id INTEGER PRIMARY KEY,
		origin TEXT,
		name TEXT NOT NULL,
		version TEXT NOT NULL,
		pkgsize INTEGER NOT NULL,
		flatsize INTEGER NOT NULL,
		cksum TEXT NOT NULL,
		path TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		// path mirrors the real column, All/Hashed/<name>-<version>~<hash10>,
		// but some fixture rows carry a deliberately malformed cksum shorter
		// than ten characters, so take what there is.
		suffix := r.cksum
		if len(suffix) > 10 {
			suffix = suffix[:10]
		}
		if _, err := db.Exec(
			`INSERT INTO packages (origin, name, version, pkgsize, flatsize, cksum, path)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"misc/"+r.name, r.name, r.version, r.pkgsize, r.pkgsize*10, r.cksum,
			"All/Hashed/"+r.name+"-"+r.version+"~"+suffix+".pkg",
		); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestOpenRepositoriesReadsEveryRepository(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{
		{"nginx", "1.24.0_2", 1234, hash64('a')},
		{"py311-setuptools", "63.1.0", 99, hash64('b')},
	})
	writeRepoDB(t, dir, "FreeBSD-ports-kmods", []fixtureRow{
		{"wifi-firmware-rtw88-kmod-rtw8821a", "20251125", 5905, hash64('c')},
	})

	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatalf("OpenRepositories() = %v", err)
	}
	if got := repo.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3 (both repositories)", got)
	}

	// The key is name-version, the same string parsePackageName produces
	// from a cache filename and packageRequest from a mirror path.
	tests := []struct {
		nameVersion string
		wantHash    string
		wantSize    int64
	}{
		{"nginx-1.24.0_2", hash64('a'), 1234},
		{"py311-setuptools-63.1.0", hash64('b'), 99},
		{"wifi-firmware-rtw88-kmod-rtw8821a-20251125", hash64('c'), 5905},
	}
	for _, tc := range tests {
		gotHash, ok := repo.ExpectedHash(tc.nameVersion)
		if !ok || gotHash != tc.wantHash {
			t.Errorf("ExpectedHash(%q) = %q, %v; want %q, true", tc.nameVersion, gotHash, ok, tc.wantHash)
		}
		gotSize, ok := repo.ExpectedFileSizeBytes(tc.nameVersion)
		if !ok || gotSize != tc.wantSize {
			t.Errorf("ExpectedFileSizeBytes(%q) = %d, %v; want %d, true", tc.nameVersion, gotSize, ok, tc.wantSize)
		}
	}
}

// Hash and size come from one row, so a package is either fully known or not
// known at all. The peer transfer spec calls the half-known state a bug.
func TestRepositoriesNeverKnowsHalfAPackage(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{{"nginx", "1.24.0_2", 1234, hash64('a')}})

	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, nv := range []string{"nginx-1.24.0_2", "absent-1.0"} {
		_, hashOK := repo.ExpectedHash(nv)
		_, sizeOK := repo.ExpectedFileSizeBytes(nv)
		if hashOK != sizeOK {
			t.Errorf("%q: hash found = %v but size found = %v; they must agree", nv, hashOK, sizeOK)
		}
	}
}

// A malformed cksum is dropped rather than served. Comparing peer bytes against
// a hash that cannot match would blacklist an honest peer for our own bad data.
func TestOpenRepositoriesSkipsMalformedRows(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{
		{"good", "1.0", 10, hash64('a')},
		{"uppercase", "1.0", 10, strings.ToUpper(hash64('b'))},
		{"tooshort", "1.0", 10, "abc"},
		{"nonhex", "1.0", 10, strings.Repeat("z", 64)},
		{"zerosize", "1.0", 0, hash64('c')},
	})

	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := repo.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1 (only the well-formed row)", got)
	}
	for _, nv := range []string{"uppercase-1.0", "tooshort-1.0", "nonhex-1.0", "zerosize-1.0"} {
		if _, ok := repo.ExpectedHash(nv); ok {
			t.Errorf("ExpectedHash(%q) found a malformed row", nv)
		}
	}
}

// The two causes are reported apart, so a pkgsize problem is not diagnosed as a
// checksum problem. Dropping is right either way; saying which is right is what
// makes the warning actionable.
func TestLoadRepositoryDatabaseSeparatesTheSkipCauses(t *testing.T) {
	dir := t.TempDir()
	path := writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{
		{"good", "1.0", 10, hash64('a')},
		{"uppercase", "1.0", 10, strings.ToUpper(hash64('b'))},
		{"nonhex", "1.0", 10, strings.Repeat("z", 64)},
		{"zerosize", "1.0", 0, hash64('c')},
		{"negativesize", "1.0", -1, hash64('d')},
	})

	loaded, skipped, err := loadRepositoryDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d row(s), want 1", len(loaded))
	}
	if got, want := namesForLog(skipped.badCksum), "nonhex-1.0, uppercase-1.0"; got != want {
		t.Errorf("badCksum = %q, want %q", got, want)
	}
	if got, want := namesForLog(skipped.badPkgSize), "negativesize-1.0, zerosize-1.0"; got != want {
		t.Errorf("badPkgSize = %q, want %q", got, want)
	}
}

// A row failing both checks is counted once, under the cksum, because that is
// the cause the reader must act on first: no hash means no verification at all.
func TestSkipCausesDoNotDoubleCount(t *testing.T) {
	dir := t.TempDir()
	path := writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{{"both", "1.0", 0, "nope"}})

	_, skipped, err := loadRepositoryDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped.badCksum) != 1 || len(skipped.badPkgSize) != 0 {
		t.Errorf("badCksum = %v, badPkgSize = %v; want the row counted once, under the cksum", skipped.badCksum, skipped.badPkgSize)
	}
}

// The cap keeps one misconfigured catalogue from turning a warning into
// thousands of lines, and the tail says how much was elided.
func TestNamesForLogCapsTheList(t *testing.T) {
	var keys []string
	for i := 0; i < loggedNameLimit+5; i++ {
		keys = append(keys, fmt.Sprintf("pkg%02d-1.0", i))
	}
	got := namesForLog(keys)
	if !strings.HasSuffix(got, " and 5 more") {
		t.Errorf("namesForLog(%d keys) = %q, want a %q tail", len(keys), got, " and 5 more")
	}
	if strings.Count(got, ",") != loggedNameLimit-1 {
		t.Errorf("namesForLog named %d key(s), want %d", strings.Count(got, ",")+1, loggedNameLimit)
	}
}

// Ratified: first repository in sorted path order wins, deterministically, and
// the colliding names are logged. Measured zero collisions across both
// repositories on the reference host.
func TestCollisionResolvesToFirstPathInOrder(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "aaa-first", []fixtureRow{{"dup", "1.0", 111, hash64('a')}})
	writeRepoDB(t, dir, "zzz-second", []fixtureRow{{"dup", "1.0", 222, hash64('b')}})

	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}
	gotHash, _ := repo.ExpectedHash("dup-1.0")
	if gotHash != hash64('a') {
		t.Errorf("ExpectedHash(dup-1.0) = %q, want the first repository in path order", gotHash)
	}
	if got, _ := repo.ExpectedFileSizeBytes("dup-1.0"); got != 111 {
		t.Errorf("ExpectedFileSizeBytes(dup-1.0) = %d, want 111 from the same row as the hash", got)
	}
}

// A daemon whose snapshot is empty verifies nothing and 404s everything. Say so
// at startup instead of leaving it to be discovered from the access log.
func TestOpenRepositoriesRejectsAnEmptyDirectory(t *testing.T) {
	if _, err := OpenRepositories(t.TempDir()); err == nil {
		t.Fatal("OpenRepositories() on a directory with no database = nil, want an error")
	}
}

func TestOpenRepositoriesRejectsAMissingDirectory(t *testing.T) {
	if _, err := OpenRepositories(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("OpenRepositories() on a missing directory = nil, want an error")
	}
}

// pkg creates a repository's directory before it first fetches the catalogue,
// so a directory with no db file is a normal state and must not be fatal.
func TestOpenRepositoriesToleratesARepositoryWithNoDatabase(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{{"nginx", "1.24.0_2", 1234, hash64('a')}})
	if err := os.MkdirAll(filepath.Join(dir, "never-fetched"), 0755); err != nil {
		t.Fatal(err)
	}

	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatalf("OpenRepositories() = %v", err)
	}
	if got := repo.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1", got)
	}
}

// The repository database is pkg's. AGENTS.md forbids writing to it, and the
// DSN is what enforces that rather than our care.
func TestRepositoriesOpensReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{{"nginx", "1.24.0_2", 1234, hash64('a')}})

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM packages`); err == nil {
		t.Error("DELETE through the read-only DSN succeeded; the catalogue must not be writable")
	}

	if _, err := OpenRepositories(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("reading the catalogue modified it")
	}
}

func TestReloadPicksUpANewPackage(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{{"nginx", "1.24.0_2", 1234, hash64('a')}})

	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.ExpectedHash("curl-8.6.0"); ok {
		t.Fatal("curl is present before it was added")
	}

	if err := os.RemoveAll(filepath.Join(dir, "FreeBSD-ports")); err != nil {
		t.Fatal(err)
	}
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{
		{"nginx", "1.24.0_2", 1234, hash64('a')},
		{"curl", "8.6.0", 42, hash64('d')},
	})

	if err := repo.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	if _, ok := repo.ExpectedHash("curl-8.6.0"); !ok {
		t.Error("Reload() did not pick up the new package")
	}
}
