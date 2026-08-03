package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

// A fixed dummy port used throughout these tests
const testListeningPort = 9000

func TestNew(t *testing.T) {
	called := false
	cw := New("/tmp/test-cache", testListeningPort, nil, func(port int, pkgs []PackageInfo) {
		called = true
	}, nil)
	if cw == nil {
		t.Fatal("New returned nil")
	}
	if cw.cacheDir != "/tmp/test-cache" {
		t.Errorf("cacheDir = %q, want %q", cw.cacheDir, "/tmp/test-cache")
	}
	if cw.listeningPort != testListeningPort {
		t.Errorf("listeningPort = %d, want %d", cw.listeningPort, testListeningPort)
	}
	if cw.onUpdate == nil {
		t.Error("onUpdate is nil")
	}
	_ = called
}

// Responsibility: verify a real, valid package on disk survives the full
// scanCache() -> sanityFilter() pipeline with its name/version/size
// correctly extracted.
//
// TestScan covers UC-05 Steps 4-5 (scan the cache, read-only; filter with
// cheap sanity checks) and, per docs/uc-05.puml, the exact
// scanCache() -> sanityFilter(packageList) call sequence shared by BOTH
// of the diagram's alt branches (the ping-triggered "tracker does not
// know this IP" path, and the cache-watcher-triggered unsolicited
// announce path). A package whose size matches the repo DB must survive
// scanning and filtering intact, with its name and version correctly
// parsed from the file name.
func TestScan(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "nginx-1.24.0.pkg"), []byte("hello"), 0644); err != nil {
		t.Fatalf("writefile: %v", err)
	}
	repoDB := newFakeRepositoryDatabase().withPackage("nginx-1.24.0", 5)

	var got []PackageInfo
	cw := New(tmpDir, testListeningPort, repoDB, func(port int, pkgs []PackageInfo) {
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

// Responsibility: verify the listening port set at construction time
// actually reaches onUpdate on every call, unchanged.
//
// TestScan_PassesListeningPortThrough covers the diagram's
// announce(listeningPort, packageList) signature directly: "The serving
// port must be in the message: the tracker takes the IP from the
// connection's source address but cannot infer the listening port." Every
// onUpdate call must carry the exact listening port the Watcher was
// constructed with, unchanged, so that whoever eventually implements the
// real announce() call has it available without hunting for it elsewhere.
func TestScan_PassesListeningPortThrough(t *testing.T) {
	tmpDir := t.TempDir()

	const wantPort = 4242
	var gotPort int
	cw := New(tmpDir, wantPort, nil, func(port int, pkgs []PackageInfo) {
		gotPort = port
	}, nil)

	if _, err := cw.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if gotPort != wantPort {
		t.Errorf("onUpdate received port = %d, want %d", gotPort, wantPort)
	}
}

// Responsibility: verify an empty cache directory produces an empty list
// without erroring — this is the deregister signal, not a failure.
//
// TestScanEmptyDir covers UC-05's Precondition and sets up the behaviour Step 7 depends on: "Empty list: tracker
// acknowledges but stores nothing." Scanning an empty cache directory
// must not error, and must simply produce an empty list.
func TestScanEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	var got []PackageInfo
	cw := New(tmpDir, testListeningPort, nil, func(port int, pkgs []PackageInfo) {
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

// Responsibility: verify a package whose on-disk size doesn't match the
// repo DB is silently dropped, never announced.
//
// TestScan_RejectsSizeMismatch covers UC-05 Step 5's size-matching sanity
// check: "file size matches the repository database entry." It also
// covers the Assumptions/Comments note that a mismatched file "costs one
// wasted transfer and a blacklist entry on the requester's side" — i.e.
// our job is specifically to NOT announce it, so that cost is never paid
// in the first place. A package whose on-disk size does not match what
// the repository database expects must be filtered out, rather than being
// announced as if it were fine. This models a truncated or corrupted file
// sitting in the cache.
func TestScan_RejectsSizeMismatch(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "curl-8.9.1.pkg"), []byte("hello"), 0644); err != nil {
		t.Fatalf("writefile: %v", err)
	}
	repoDB := newFakeRepositoryDatabase().withPackage("curl-8.9.1", 99999)

	var got []PackageInfo
	cw := New(tmpDir, testListeningPort, repoDB, func(port int, pkgs []PackageInfo) {
		got = pkgs
	}, nil)

	if _, err := cw.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected the size-mismatched package to be filtered out, got %d packages", len(got))
	}
}

// Responsibility: verify a file that doesn't look like a real package
// (bad name format) is silently dropped, never announced.
//
// TestScan_RejectsGarbageFileName covers UC-05 Step 5's other sanity
// check: "valid name-version filename." A file that doesn't look like a
// valid "name-version" package must never show
// up in the announced list, regardless of the repository database.
func TestScan_RejectsGarbageFileName(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "somejunk.lock"), []byte("x"), 0644); err != nil {
		t.Fatalf("writefile: %v", err)
	}

	var got []PackageInfo
	cw := New(tmpDir, testListeningPort, newFakeRepositoryDatabase(), func(port int, pkgs []PackageInfo) {
		got = pkgs
	}, nil)

	if _, err := cw.Scan(); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected the garbage file name to be filtered out, got %d packages", len(got))
	}
}

// Responsibility: verify Start() and Stop() work cleanly and Stop() can
// safely be called twice without panicking.
//
// TestStartAndStop covers UC-05's Precondition ("Daemon is running") and
// general lifecycle robustness: the watcher must start cleanly and shut
// down cleanly, including tolerating a repeated Stop() call (relevant for
// UC-05 Error State "Network error mid-announce" -> "Daemon logs the error
// and schedules a retry" flows, where shutdown can race with a pending
// retry).
func TestStartAndStop(t *testing.T) {
	tmpDir := t.TempDir()

	cw := New(tmpDir, testListeningPort, nil, func(port int, pkgs []PackageInfo) {}, nil)
	if err := cw.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	cw.Stop()
	cw.Stop()
}

// Responsibility: verify filename-to-name/version splitting is correct,
// including the no-version edge case that used to be buggy.
//
// TestParsePackageName covers the "valid name-version filename" half of
// UC-05 Step 5 at the unit level: packages are identified by name-version
// strings throughout the spec (e.g. UC-02 Step 5: "IWant(packageName-version)"),
// so correctly splitting a file name into name + version is foundational
// to every other check in this file. Includes the no-version edge case
// that an earlier version of this function got wrong.
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

// Responsibility: verify the fsnotify loop actually detects a real
// filesystem change and fires an event end-to-end (not just Scan()
// called directly).
//
// TestChangeEvent covers UC-05's Trigger clause: "Cache watcher sees a new
// package appear in the pkg cache," and Step 9: "When the cache watcher
// sees a new package, the daemon announces directly without waiting to be
// asked." It exercises the actual fsnotify-driven path end-to-end (not
// just a direct Scan() call), confirming that a real filesystem change is
// detected and reported.
func TestChangeEvent(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "test-1.0.0.pkg"), []byte("x"), 0644)

	var events []ChangeEvent
	cw := New(tmpDir, testListeningPort, nil, func(port int, pkgs []PackageInfo) {}, func(ev ChangeEvent) {
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

// Responsibility: verify calling Stop() twice never panics or hangs.
//
// TestStopIdempotent covers general lifecycle robustness required for the
// daemon to run reliably under UC-05's "keeps itself registered through
// periodic pings" description — the watcher must never panic or hang if
// shutdown is triggered more than once (e.g. once from a signal handler,
// once from a deferred cleanup call).
func TestStopIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	cw := New(tmpDir, testListeningPort, nil, func(port int, pkgs []PackageInfo) {}, nil)
	if err := cw.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	cw.Stop()
	cw.Stop()
}
