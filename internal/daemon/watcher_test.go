package daemon

import (
	"os"
	"path/filepath"
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
	}
	for _, tt := range tests {
		name, ver := parsePackageName(tt.filename)
		if name != tt.wantName || ver != tt.wantVer {
			t.Errorf("parsePackageName(%q) = (%q, %q), want (%q, %q)",
				tt.filename, name, ver, tt.wantName, tt.wantVer)
		}
	}
}

func TestChangeEvent(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "test-1.0.0.pkg"), []byte("x"), 0644)

	var events []ChangeEvent
	cw := New(tmpDir, nil, func(pkgs []PackageInfo) {}, func(ev ChangeEvent) {
		events = append(events, ev)
	})

	if err := cw.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	os.WriteFile(filepath.Join(tmpDir, "new-2.0.0.pkg"), []byte("y"), 0644)
	time.Sleep(100 * time.Millisecond)

	cw.Stop()

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
