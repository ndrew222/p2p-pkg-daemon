package daemon

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
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

// captureLog redirects the standard logger for the duration of one test and
// returns the accumulated output. The reader reports collisions and dropped
// rows only to the log -- they are diagnostics, not values a caller acts on --
// so the log is the only place a test can observe them.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	out := log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	return buf.String
}

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

	logged := captureLog(t)
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

	// The two causes are counted and reported separately: a pkgsize problem
	// diagnosed as a checksum problem sends the reader to the wrong column.
	out := logged()
	if !strings.Contains(out, "skipped 3 row(s) whose cksum") {
		t.Errorf("want 3 rows dropped for a bad cksum; got:\n%s", out)
	}
	if !strings.Contains(out, "skipped 1 row(s) whose pkgsize") {
		t.Errorf("want 1 row dropped for a bad pkgsize; got:\n%s", out)
	}
	// zerosize-1.0 has a well-formed cksum, so it must be attributed to
	// pkgsize alone rather than swept into the checksum count.
	cksumLine, sizeLine := logLineContaining(out, "cksum"), logLineContaining(out, "pkgsize")
	if strings.Contains(cksumLine, "zerosize-1.0") {
		t.Errorf("zerosize-1.0 attributed to the cksum cause: %s", cksumLine)
	}
	if !strings.Contains(sizeLine, "zerosize-1.0") {
		t.Errorf("zerosize-1.0 not named on the pkgsize line: %s", sizeLine)
	}
}

// A row can fail both checks at once. It is attributed to the cksum -- the
// first cause it fails, and the one to act on, since no hash means no
// verification at all -- and must not also appear under pkgsize, or the two
// lists stop partitioning the dropped rows and one bad row is reported twice.
// This is the case that regresses silently if the switch is ever folded back
// into a single `||`.
func TestSkipCausesDoNotDoubleCount(t *testing.T) {
	dir := t.TempDir()
	// The well-formed row is load-bearing: Reload rejects an empty snapshot,
	// so a catalogue of nothing but the malformed row fails before the
	// diagnostic under test is reached.
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{
		{"good", "1.0", 10, hash64('a')},
		{"both", "1.0", 0, "nope"},
	})

	logged := captureLog(t)
	if _, err := OpenRepositories(dir); err != nil {
		t.Fatal(err)
	}
	out := logged()
	if !strings.Contains(out, "skipped 1 row(s) whose cksum") {
		t.Errorf("want the doubly-malformed row counted once, under cksum; got:\n%s", out)
	}
	if strings.Contains(out, "whose pkgsize") {
		t.Errorf("doubly-malformed row also reported under pkgsize; got:\n%s", out)
	}
	if line := logLineContaining(out, "cksum"); !strings.Contains(line, "both-1.0") {
		t.Errorf("cksum line does not name both-1.0: %s", line)
	}
}

// logLineContaining returns the first line of out holding want, or "".
func logLineContaining(out, want string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	return ""
}

// Ratified: first repository in sorted path order wins, deterministically, and
// the colliding names are logged so a wrong pick is diagnosable. Measured zero
// collisions across both repositories on the reference host.
func TestCollisionResolvesToFirstPathInOrder(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "aaa-first", []fixtureRow{{"dup", "1.0", 111, hash64('a')}})
	writeRepoDB(t, dir, "zzz-second", []fixtureRow{{"dup", "1.0", 222, hash64('b')}})

	logged := captureLog(t)
	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Naming the collision is the whole diagnostic: a wrong pick blacklists
	// an honest peer, and the log is the only thread back to why.
	if out := logged(); !strings.Contains(out, "dup-1.0") {
		t.Errorf("collision log does not name the colliding package; got:\n%s", out)
	}
	gotHash, _ := repo.ExpectedHash("dup-1.0")
	if gotHash != hash64('a') {
		t.Errorf("ExpectedHash(dup-1.0) = %q, want the first repository in path order", gotHash)
	}
	if got, _ := repo.ExpectedFileSizeBytes("dup-1.0"); got != 111 {
		t.Errorf("ExpectedFileSizeBytes(dup-1.0) = %d, want 111 from the same row as the hash", got)
	}
}

// One misconfigured repository can shadow ports wholesale, so the diagnostic
// has to stay a diagnostic rather than dumping 38,000 names into the log.
//
// The rows DISAGREE, which after ADR-010 is what gets reported at all: a
// duplicate that agrees is the normal state once pkg writes jmj's own catalogue
// into repo_db_dir, and logging those meant 37,813 lines per reload.
func TestCollisionLogIsCapped(t *testing.T) {
	dir := t.TempDir()
	var rows, conflicting []fixtureRow
	for i := 0; i < logNameLimit+5; i++ {
		rows = append(rows, fixtureRow{fmt.Sprintf("dup%02d", i), "1.0", 10, hash64('a')})
		conflicting = append(conflicting, fixtureRow{fmt.Sprintf("dup%02d", i), "1.0", 20, hash64('b')})
	}
	writeRepoDB(t, dir, "aaa-first", rows)
	writeRepoDB(t, dir, "zzz-second", conflicting)

	logged := captureLog(t)
	if _, err := OpenRepositories(dir); err != nil {
		t.Fatal(err)
	}
	out := logged()
	if !strings.Contains(out, "and 5 more") {
		t.Errorf("collision log is not capped with an \"and N more\" tail; got:\n%s", out)
	}
	// The tail replaces the surplus names, it does not merely follow them.
	if strings.Contains(out, "dup14-1.0") {
		t.Errorf("collision log names a package past the cap; got:\n%s", out)
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

// ADR-008 relies on this: at runtime a reload failure logs and keeps what it
// has, because the alternative to a stale catalogue is no catalogue at all.
func TestReloadFailureKeepsThePreviousSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{{"nginx", "1.24.0_2", 1234, hash64('a')}})

	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Every catalogue disappears -- the shape of a reload that lands
	// mid-rewrite.
	if err := os.RemoveAll(filepath.Join(dir, "FreeBSD-ports")); err != nil {
		t.Fatal(err)
	}
	if err := repo.Reload(); err == nil {
		t.Fatal("Reload() = nil for a directory with no catalogue, want an error")
	}

	if _, ok := repo.ExpectedHash("nginx-1.24.0_2"); !ok {
		t.Error("a failed Reload discarded the previous snapshot")
	}
	if got := repo.Len(); got != 1 {
		t.Errorf("Len() = %d after a failed Reload, want 1", got)
	}
}

// writeRepoSource records the repository URL pkg stores in repodata, which is
// what identifies a catalogue as this daemon's own (ADR-010). Measured shape on
// the reference host: repodata holds key/value pairs and the key is
// "packagesite".
func writeRepoSource(t *testing.T, dir, repo, url string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, repo, repoDBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS repodata (key TEXT UNIQUE NOT NULL, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO repodata (key, value) VALUES ('packagesite', ?)`, url); err != nil {
		t.Fatal(err)
	}
}

// ADR-010: pkg resolved the package from the jmj repository, so jmj's row is
// the one pkg is acting on -- even though "jmj" sorts after "FreeBSD-ports" and
// path order would have picked the other one.
func TestOwnCatalogueWinsACollision(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{{"dup", "1.0", 111, hash64('a')}})
	writeRepoDB(t, dir, "jmj", []fixtureRow{{"dup", "1.0", 222, hash64('b')}})
	writeRepoSource(t, dir, "FreeBSD-ports", "pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly")
	writeRepoSource(t, dir, "jmj", "http://127.0.0.1:9001")

	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.ExpectedHash("dup-1.0"); got != hash64('b') {
		t.Errorf("ExpectedHash(dup-1.0) = %q, want the row from our own catalogue", got)
	}
	if got, _ := repo.ExpectedFileSizeBytes("dup-1.0"); got != 222 {
		t.Errorf("ExpectedFileSizeBytes(dup-1.0) = %d, want 222 from the same row as the hash", got)
	}
}

// The fallback must be untouched, because that is what every host which has not
// adopted jmj is running -- and what this one runs on its first start, before
// pkg has written our catalogue at all.
func TestPathOrderStillDecidesWithoutAnOwnCatalogue(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "aaa-first", []fixtureRow{{"dup", "1.0", 111, hash64('a')}})
	writeRepoDB(t, dir, "zzz-second", []fixtureRow{{"dup", "1.0", 222, hash64('b')}})
	writeRepoSource(t, dir, "aaa-first", "pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly")
	writeRepoSource(t, dir, "zzz-second", "pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/kmods_quarterly_1")

	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.ExpectedHash("dup-1.0"); got != hash64('a') {
		t.Errorf("ExpectedHash(dup-1.0) = %q, want the first repository in path order", got)
	}
}

// The deployment's normal state: jmj's catalogue is a copy of the repository it
// fronts, so every row duplicates and every duplicate agrees. Logging those is
// what teaches an operator to ignore the log.
func TestAgreeingDuplicatesAreNotLogged(t *testing.T) {
	dir := t.TempDir()
	rows := []fixtureRow{
		{"nginx", "1.24.0_2", 1234, hash64('a')},
		{"curl", "8.6.0", 42, hash64('d')},
	}
	writeRepoDB(t, dir, "FreeBSD-ports", rows)
	writeRepoDB(t, dir, "jmj", rows)
	writeRepoSource(t, dir, "jmj", "http://127.0.0.1:9001")

	logged := captureLog(t)
	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out := logged(); strings.Contains(out, "DISAGREE") {
		t.Errorf("identical duplicate rows were reported as a conflict; got:\n%s", out)
	}
	if got := repo.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2 -- duplicates collapse to one row each", got)
	}
}

// And the case the alarm exists for: the two catalogues have drifted, which is
// where picking the wrong row makes us blacklist an honest peer.
func TestDisagreeingDuplicatesAreLoggedOnce(t *testing.T) {
	dir := t.TempDir()
	writeRepoDB(t, dir, "FreeBSD-ports", []fixtureRow{
		{"nginx", "1.24.0_2", 1234, hash64('a')},
		{"curl", "8.6.0", 42, hash64('d')},
	})
	writeRepoDB(t, dir, "jmj", []fixtureRow{
		{"nginx", "1.24.0_2", 1299, hash64('c')}, // rebuilt: same version, new bytes
		{"curl", "8.6.0", 42, hash64('d')},
	})
	writeRepoSource(t, dir, "jmj", "http://127.0.0.1:9001")

	logged := captureLog(t)
	repo, err := OpenRepositories(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := logged()
	if !strings.Contains(out, "nginx-1.24.0_2") {
		t.Errorf("the drifted package is not named in the log; got:\n%s", out)
	}
	if strings.Contains(out, "curl-8.6.0") {
		t.Errorf("a package whose rows agree was reported as a conflict; got:\n%s", out)
	}
	// Our row still wins, which is the point of reporting rather than refusing.
	if got, _ := repo.ExpectedHash("nginx-1.24.0_2"); got != hash64('c') {
		t.Errorf("ExpectedHash = %q, want our own catalogue's row despite the conflict", got)
	}
}

func TestOwnCatalogueFirst(t *testing.T) {
	const ours = "/db/jmj/db"
	tests := []struct {
		name    string
		paths   []string
		sources map[string]string
		want    []string
	}{
		{
			name:    "no sources at all leaves the order alone",
			paths:   []string{"/db/a/db", "/db/b/db"},
			sources: map[string]string{},
			want:    []string{"/db/a/db", "/db/b/db"},
		},
		{
			name:  "no loopback source leaves the order alone",
			paths: []string{"/db/a/db", "/db/b/db"},
			sources: map[string]string{
				"/db/a/db": "pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly",
				"/db/b/db": "https://example.invalid/repo",
			},
			want: []string{"/db/a/db", "/db/b/db"},
		},
		{
			name:    "ours moves to the front",
			paths:   []string{"/db/a/db", ours, "/db/z/db"},
			sources: map[string]string{ours: "http://127.0.0.1:9001"},
			want:    []string{ours, "/db/a/db", "/db/z/db"},
		},
		{
			name:    "localhost counts as ours",
			paths:   []string{"/db/a/db", ours},
			sources: map[string]string{ours: "http://localhost:9001"},
			want:    []string{ours, "/db/a/db"},
		},
		{
			// Degenerate -- jmj fronts one repository (ADR-007) -- but it
			// must not fall to map iteration order.
			name:  "several loopback catalogues keep path order between them",
			paths: []string{"/db/a/db", "/db/m/db", "/db/z/db"},
			sources: map[string]string{
				"/db/z/db": "http://127.0.0.1:9001",
				"/db/m/db": "http://127.0.0.2:9001",
			},
			want: []string{"/db/m/db", "/db/z/db", "/db/a/db"},
		},
		{
			name:    "an unparsable source is not ours",
			paths:   []string{"/db/a/db", "/db/b/db"},
			sources: map[string]string{"/db/b/db": "::not a url::"},
			want:    []string{"/db/a/db", "/db/b/db"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ownCatalogueFirst(slices.Clone(tc.paths), tc.sources)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ownCatalogueFirst() = %v, want %v", got, tc.want)
			}
		})
	}
}
