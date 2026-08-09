package daemon

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The layout measured on FreeBSD 15.1-RELEASE-p1: the cache is flat, the real
// file carries the ~hash10 suffix, and an unsuffixed symlink sits beside it
// (docs/logs/claude-pkg-mirror-verification.md §7.5). The seeder resolves the
// unsuffixed name, so it must follow that link and report the TARGET's size --
// not the 32-byte length of the link itself, which is the mistake the cache
// watcher had to be fixed for in the opposite direction.
func TestCacheSourceServesThePkgCacheLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows; the layout under test is FreeBSD's")
	}
	cache := t.TempDir()
	content := []byte("libpaper package bytes, longer than a link target string would be")
	real := "libpaper-1.1.28_1~599a5a67ab.pkg"
	if err := os.WriteFile(filepath.Join(cache, real), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(cache, "libpaper-1.1.28_1.pkg")); err != nil {
		t.Fatal(err)
	}

	f, size, ok := NewCacheSource(cache).Open("libpaper-1.1.28_1")
	if !ok {
		t.Fatal("Open() on a cached package = not held")
	}
	defer f.Close()

	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d (the target's size, not the link's)", size, len(content))
	}
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

// A name-version arrives from an untrusted remote daemon. peer.validName is a
// deliberately minimal wire check -- it rejects only empty, oversized and
// control-character input -- so everything below reaches this code intact, and
// this is the boundary that has to refuse it.
//
// Refused, not cleaned: a cleaned path is a guess at what the caller meant,
// and the caller here is a hostile peer.
func TestCacheSourceRefusesAnythingThatIsNotAPlainFileName(t *testing.T) {
	cache := t.TempDir()
	if err := os.WriteFile(filepath.Join(cache, "jq-1.7.pkg"), []byte("held"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file the traversal attempts are aiming at, one level up.
	outside := filepath.Dir(cache)
	secret := filepath.Join(outside, "secret.pkg")
	if err := os.WriteFile(secret, []byte("must never be served"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(secret) })

	src := NewCacheSource(cache)
	for _, nv := range []string{
		"",
		".",
		"..",
		"../secret",
		"../../etc/passwd",
		"/etc/passwd",
		"sub/dir",
		`sub\dir`,
		"jq-1.7/../../secret",
		"with\x00nul",
	} {
		t.Run(nv, func(t *testing.T) {
			f, _, ok := src.Open(nv)
			if ok {
				f.Close()
				t.Fatalf("Open(%q) served something; a name-version off the wire must never steer the path", nv)
			}
		})
	}

	// The control: the boundary refuses hostile input without refusing
	// legitimate input.
	f, _, ok := src.Open("jq-1.7")
	if !ok {
		t.Fatal("Open() refused a package that is genuinely held")
	}
	f.Close()
}

// Not held is the ordinary case, not an error: pkg clean removes packages we
// announced. It answers 404 and the seeder re-announces (UC-06 §5b).
func TestCacheSourceReportsNotHeld(t *testing.T) {
	src := NewCacheSource(t.TempDir())
	if f, _, ok := src.Open("never-1.0"); ok {
		f.Close()
		t.Error("Open() on an empty cache reported the package as held")
	}

	// A missing cache directory is the same answer rather than a panic. The
	// directory is checked at startup by config.Validate and by the cache
	// watcher; re-reporting it per request would only duplicate a failure
	// already surfaced.
	src = NewCacheSource(filepath.Join(t.TempDir(), "no-such-cache"))
	if f, _, ok := src.Open("never-1.0"); ok {
		f.Close()
		t.Error("Open() against a missing cache directory reported a package as held")
	}
}

// A directory named like a package must not be served as one.
func TestCacheSourceServesOnlyRegularFiles(t *testing.T) {
	cache := t.TempDir()
	if err := os.Mkdir(filepath.Join(cache, "weird-1.0.pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if f, _, ok := NewCacheSource(cache).Open("weird-1.0"); ok {
		f.Close()
		t.Error("Open() served a directory")
	}
}

// The pkg cache is READ-ONLY to this daemon, always. The watcher once called
// MkdirAll on it and that was a hard-constraint violation; the seeder must not
// reinstate it by another route.
func TestCacheSourceNeverWritesToTheCache(t *testing.T) {
	parent := t.TempDir()
	cache := filepath.Join(parent, "cache")
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "jq-1.7.pkg"), []byte("held"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := listing(t, cache)

	src := NewCacheSource(cache)
	for _, nv := range []string{"jq-1.7", "absent-1.0", "../escape", "sub/dir"} {
		if f, _, ok := src.Open(nv); ok {
			io.Copy(io.Discard, f)
			f.Close()
		}
	}

	if after := listing(t, cache); after != before {
		t.Errorf("the cache changed: %q -> %q; it is read-only to this daemon", before, after)
	}

	// And nothing was created beside it either -- a source pointed at a
	// missing directory must not bring it into existence.
	missing := filepath.Join(parent, "not-a-cache")
	if f, _, ok := NewCacheSource(missing).Open("jq-1.7"); ok {
		f.Close()
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("Open() created %q; the daemon never creates the pkg cache", missing)
	}
}

func listing(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}
