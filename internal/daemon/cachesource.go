package daemon

// The production peer.PackageSource: the seeding side's read-only view of
// pkg's package cache (UC-06).
//
// Until this landed there was no production PackageSource anywhere in the
// tree. The only implementors were test fakes and cmd/demo's in-memory store,
// so the daemon announced a serving port that nothing listened on and every
// peer acting on our tracker entry got connection-refused.
//
// It lives here rather than in internal/peer because resolving a name-version
// to a file in pkg's cache is pkg-cache knowledge -- the ".pkg" extension and
// the "~hash10" suffix -- and that knowledge already lives in watcher.go,
// shared with the facade. internal/peer stays free of it and keeps a
// one-method interface.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CacheSource serves package bytes straight out of pkg's cache.
//
// READ-ONLY, always. Nothing here creates, writes, renames or removes
// anything: the cache belongs to pkg. The watcher once called MkdirAll on this
// directory and that was a hard-constraint violation.
//
// Zero allocation of package content: Open returns the *os.File itself, so
// http.ServeContent can stream it and the runtime can use sendfile. The 2.83
// GiB package is served from a 1 GiB host without ever being resident.
type CacheSource struct {
	dir string
}

// NewCacheSource returns a source over cacheDir, which is config.CacheDir.
//
// The directory is not stat'd here. It is checked at startup by
// config.Validate and by the cache watcher, both of which refuse to run
// without it; re-checking per source would only duplicate a failure that has
// already been reported, and checking once at construction would say nothing
// about the directory's state at request time anyway.
func NewCacheSource(cacheDir string) *CacheSource {
	return &CacheSource{dir: cacheDir}
}

// Open implements peer.PackageSource.
//
// The path it opens is <cache_dir>/<name-version>.pkg. On a real host that is
// the symlink pkg leaves beside the real file -- measured on FreeBSD
// 15.1-RELEASE-p1, where `find /var/cache/pkg -type d` returns only the cache
// directory itself and an install of libpaper-1.1.28_1 leaves
//
//	libpaper-1.1.28_1.pkg -> libpaper-1.1.28_1~599a5a67ab.pkg
//
// (docs/logs/claude-pkg-mirror-verification.md §7.5). Following that link is
// correct and is what makes an O(1) lookup possible: the alternative, scanning
// the directory for a file whose parsed name-version matches, would hand a
// hostile peer a directory read per bogus request over a cache of tens of
// thousands of entries.
//
// A name-version that does not resolve is simply not held: 404, and the caller
// re-announces (UC-06 §5b). No hash is computed -- the requester verifies end
// to end, and hashing here would be wasted I/O on bytes we are not trusted for
// anyway.
func (c *CacheSource) Open(nameVersion string) (io.ReadSeekCloser, int64, bool) {
	name, ok := cacheFileName(nameVersion)
	if !ok {
		return nil, 0, false
	}

	f, err := os.Open(filepath.Join(c.dir, name))
	if err != nil {
		return nil, 0, false
	}

	// Stat through the open descriptor, so what is measured is what will
	// be served. os.Open follows the symlink, so this reports the real
	// file's size and not the 32-byte length of a link target -- the same
	// distinction that makes the watcher use Lstat for the opposite
	// purpose.
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, false
	}
	return f, info.Size(), true
}

// cacheFileName turns a name-version off the wire into a bare file name, or
// refuses.
//
// This is the path-safety boundary, and it has to be here: peer.validName is a
// deliberately minimal wire check that rejects only empty, oversized and
// control-character input, so "../../etc/passwd" reaches this function intact.
// Anything that could steer the join out of the cache directory -- a
// separator, a volume name, a dot segment -- is refused outright rather than
// cleaned, because a cleaned path is a guess at what the caller meant and the
// caller here is an untrusted remote daemon.
func cacheFileName(nameVersion string) (string, bool) {
	if nameVersion == "" || nameVersion == "." || nameVersion == ".." {
		return "", false
	}
	if strings.ContainsAny(nameVersion, `/\`) || strings.ContainsRune(nameVersion, 0) {
		return "", false
	}
	// filepath.Base collapses anything the checks above missed on a
	// platform with a path grammar we did not anticipate; if it changes the
	// string at all, the string was not a plain file name.
	name := nameVersion + packageFileExtension
	if filepath.Base(name) != name {
		return "", false
	}
	return name, true
}
