package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeRepositoryDatabase is a tiny in-memory stand-in for pkg's real
// repository database, used only in tests. You tell it what size each
// package "should" be, and it answers ExpectedFileSizeBytes() from that.
type fakeRepositoryDatabase struct {
	expectedSizesByNameVersion map[string]int64
}

func newFakeRepositoryDatabase() *fakeRepositoryDatabase {
	return &fakeRepositoryDatabase{expectedSizesByNameVersion: make(map[string]int64)}
}

func (f *fakeRepositoryDatabase) withPackage(nameVersion string, expectedSizeBytes int64) *fakeRepositoryDatabase {
	f.expectedSizesByNameVersion[nameVersion] = expectedSizeBytes
	return f
}

func (f *fakeRepositoryDatabase) ExpectedFileSizeBytes(nameVersion string) (int64, bool) {
	size, found := f.expectedSizesByNameVersion[nameVersion]
	return size, found
}

func TestNew(t *testing.T) {
	called := false
	cw := New("/tmp/test-cache", nil, func(pkgs []PackageInfo) {
		called = true
	}, nil)
	if cw == nil {
		t.Fatal("New returned nil")
	}
	if cw.cacheDir != "/tmp/test-cache" {
		t.Errorf("cacheDir = %q, want %q", cw.cacheDir, "/tmp/test-cache")
	}
	if cw.onUpdate == nil {
		t.Error("onUpdate is nil")
	}
	_ = called
}

func TestScan(t *testing.T) {
	tmpDir := t.TempDir()

	// so this package passes the sanity filter and shows up in the result.
	if err := os.WriteFile(filepath.Join(tmpDir, "nginx-1.24.0.pkg"), []byte("hello"), 0644); err != nil {
		t.Fatalf("writefile: %v", err)
	}
	repoDB := newFakeRepositoryDatabase().withPackage("nginx-1.24.0", 5)

	var got []PackageInfo
	cw := New(tmpDir, repoDB, func(pkgs []PackageInfo) {
		got = pkgs
	}, nil)

	_, err := cw.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("expected 1 package, got %d", len(got))
	}
	if got[0].Name != "nginx" {
		t.Errorf("Name = %q, want %q", got[0].Name, "nginx")
	}
	if got[0].Version != "1.24.0" {
		t.Errorf("Version = %q, want %q", got[0].Version, "1.24.0")
	}
	if got[0].FileSizeBytes != 5 {
		t.Errorf("FileSizeBytes = %d, want %d", got[0].FileSizeBytes, 5)
	}
}

func TestScanEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	var got []PackageInfo
	cw := New(tmpDir, nil, func(pkgs []PackageInfo) {
		got = pkgs
	}, nil)

	_, err := cw.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected 0 packages, got %d", len(got))
	}
}

// TestScan_RejectsSizeMismatch makes sure a package whose on-disk size does
// not match what the repository database expects is filtered out, rather
// than being announced as if it were fine. This models a truncated or
// corrupted file sitting in the cache.
func TestScan_RejectsSizeMismatch(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "curl-8.9.1.pkg"), []byte("hello"), 0644); err != nil {
		t.Fatalf("writefile: %v", err)
	}
	// Repo DB expects a completely different size than the 5-byte file we
	// actually wrote.
	repoDB := newFakeRepositoryDatabase().withPackage("curl-8.9.1", 99999)

	var got []PackageInfo
	cw := New(tmpDir, repoDB, func(pkgs []PackageInfo) {
		got = pkgs
	}, nil)

	if _, err := cw.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected the size-mismatched package to be filtered out, got %d packages", len(got))
	}
}

// TestScan_RejectsGarbageFileName makes sure a file that doesn't look like
// a valid "name-version" package (e.g. a stray lock file) never shows up
// in the announced list, regardless of the repository database.
func TestScan_RejectsGarbageFileName(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "somejunk.lock"), []byte("x"), 0644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	var got []PackageInfo
	cw := New(tmpDir, newFakeRepositoryDatabase(), func(pkgs []PackageInfo) {
		got = pkgs
	}, nil)

	if _, err := cw.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected the garbage file name to be filtered out, got %d packages", len(got))
	}
}

func TestStartAndStop(t *testing.T) {
	tmpDir := t.TempDir()

	cw := New(tmpDir, nil, func(pkgs []PackageInfo) {}, nil)
	if err := cw.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	cw.Stop()
	cw.Stop()
}

func TestParsePackageName(t *testing.T) {
	tests := []struct {
		filename string
		wantName string
		wantVer  string
	}{
		{"nginx-1.24.0.pkg", "nginx", "1.24.0"},
		{"my-pkg-2.0.1.pkg", "my-pkg", "2.0.1"},
		{"no-version.pkg", "no-version", ""},

		// The ~hash10 suffix pkg puts on the real cache file. It is the
		// first 10 characters of cksum, never part of the identifier: a
		// peer asks for "indexinfo-0.3.1_1" and nothing else.
		{"indexinfo-0.3.1_1~ae9dce33aa.pkg", "indexinfo", "0.3.1_1"},
		{"py311-setuptools-63.1.0~abcdef0123.pkg", "py311-setuptools", "63.1.0"},

		// Exactly 10 lowercase hex, or it is part of the version.
		{"curl-8.6.0~abc.pkg", "curl", "8.6.0~abc"},
		{"curl-8.6.0~zzzzzzzzzz.pkg", "curl", "8.6.0~zzzzzzzzzz"},
		{"curl-8.6.0~ABCDEF0123.pkg", "curl", "8.6.0~ABCDEF0123"},
		{"curl-8.6.0~01234567890.pkg", "curl", "8.6.0~01234567890"},
	}
	for _, tt := range tests {
		name, ver := parsePackageName(tt.filename)
		if name != tt.wantName || ver != tt.wantVer {
			t.Errorf("parsePackageName(%q) = (%q, %q), want (%q, %q)",
				tt.filename, name, ver, tt.wantName, tt.wantVer)
		}
	}
}

// The real pkg cache is flat and full of symlinks: the unsuffixed name is a
// link to the ~hash10 file beside it. Both names parse to the same
// identifier, so taking only the real file announces the package exactly
// once, at its true size.
//
// This is the bug the size check would otherwise hit: filepath.Walk uses
// Lstat, so a symlink reports the length of its target string (32 bytes for a
// 5905-byte package), which never matches the repository database.
func TestScanSkipsSymlinks(t *testing.T) {
	tmpDir := t.TempDir()

	const real = "indexinfo-0.3.1_1~ae9dce33aa.pkg"
	content := []byte("the actual package bytes")
	if err := os.WriteFile(filepath.Join(tmpDir, real), content, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(tmpDir, "indexinfo-0.3.1_1.pkg")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	repoDB := newFakeRepositoryDatabase().withPackage("indexinfo-0.3.1_1", int64(len(content)))
	cw := New(tmpDir, repoDB, nil, nil)

	got, err := cw.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Scan() returned %d packages, want 1: %+v", len(got), got)
	}
	if nv := got[0].NameVersion(); nv != "indexinfo-0.3.1_1" {
		t.Errorf("NameVersion() = %q, want %q", nv, "indexinfo-0.3.1_1")
	}
	if got[0].Path != filepath.Join(tmpDir, real) {
		t.Errorf("Path = %q, want the real file, not the link", got[0].Path)
	}
	if got[0].FileSizeBytes != int64(len(content)) {
		t.Errorf("FileSizeBytes = %d, want %d (the target's size, not the link's)",
			got[0].FileSizeBytes, len(content))
	}
}

// A cache holding only the symlink -- with its target outside the cache, or
// missing -- announces nothing rather than announcing a size that cannot be
// checked.
func TestScanSkipsDanglingSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Symlink("nowhere-1.0.0.pkg", filepath.Join(tmpDir, "curl-8.6.0.pkg")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	cw := New(tmpDir, newFakeRepositoryDatabase().withPackage("curl-8.6.0", 5), nil, nil)
	got, err := cw.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Scan() returned %+v, want nothing", got)
	}
}

func TestChangeEvent(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "test-1.0.0.pkg"), []byte("x"), 0644)

	// onChange fires on the watcher's own goroutine, so the slice needs a lock
	// on both sides: Stop() does not establish a happens-before edge with the
	// test goroutine, and -race flags the unsynchronised read otherwise.
	var (
		mu     sync.Mutex
		events []ChangeEvent
	)
	cw := New(tmpDir, nil, func(pkgs []PackageInfo) {}, func(ev ChangeEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	})

	if err := cw.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	os.WriteFile(filepath.Join(tmpDir, "new-2.0.0.pkg"), []byte("y"), 0644)
	time.Sleep(100 * time.Millisecond)

	cw.Stop()

	mu.Lock()
	defer mu.Unlock()

	foundAdded := false
	for _, ev := range events {
		if ev.Type == Added && ev.Package.Name == "new" && ev.Package.Version == "2.0.0" {
			foundAdded = true
			break
		}
	}
	if !foundAdded {
		t.Errorf("expected Added event for new-2.0.0.pkg, got events: %+v", events)
	}
}

func TestStopIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	cw := New(tmpDir, nil, func(pkgs []PackageInfo) {}, nil)
	if err := cw.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	cw.Stop()
	cw.Stop()
}
