package peer

// Shared helpers for the peer wire tests.
//
// Everything here runs on any OS: no FreeBSD, no pkg, no second machine. The
// "pkg cache" is a directory of ordinary files and the "repository database"
// is a Want computed in the test.

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// dirSource is a PackageSource over a directory whose files are named exactly
// by name-version. It stands in for the pkg cache.
//
// The production source is daemon.CacheSource, which cannot be used here:
// internal/daemon imports internal/peer, so the dependency only runs one way.
// What matters for these tests is the shape of the contract -- an open handle
// and a size, never a byte slice -- and this reproduces it exactly.
type dirSource string

func (d dirSource) Open(nameVersion string) (io.ReadSeekCloser, int64, bool) {
	if nameVersion == "" || strings.ContainsAny(nameVersion, `/\`) {
		return nil, 0, false
	}
	f, err := os.Open(filepath.Join(string(d), nameVersion))
	if err != nil {
		return nil, 0, false
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, false
	}
	return f, info.Size(), true
}

// writePackage puts content in the stand-in cache under nameVersion and
// returns what pkg's repository database would say about it: the two columns
// of one row, which is why they travel together.
func writePackage(t *testing.T, dir, nameVersion string, content []byte) Want {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, nameVersion), content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return Want{Hash: hex.EncodeToString(sum[:]), Size: int64(len(content))}
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hasher hashes incrementally, so a test fixture can be built without holding
// the thing it is hashing.
type hasher struct{ h hash.Hash }

func newHasher() *hasher         { return &hasher{h: sha256.New()} }
func (s *hasher) Write(p []byte) { s.h.Write(p) }
func (s *hasher) hex() string    { return hex.EncodeToString(s.h.Sum(nil)) }

// startSeeder runs a real peer.Server over an httptest server and returns its
// host:port, which is the form the tracker hands out and FetchFromPeer takes.
func startSeeder(t *testing.T, srv *Server) string {
	t.Helper()
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

// startRawPeer runs an arbitrary handler as a peer. Used for the hostile cases
// a real seeder will never produce -- a lying Content-Length, an overlong
// body, a bare status code.
func startRawPeer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

// assertTempDirEmpty is the other half of every rejection test: a failed
// transfer must leave nothing behind. temp_dir is scratch, not a cache, and a
// spool that outlives its request is a store the daemon is not allowed to have.
func assertTempDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("temp_dir still holds %v; the spool must not outlive the request", names)
	}
}

// blockingReader is a package body whose Read parks until released, so a test
// can hold a seeding slot open and observe what the next request gets. Seek
// works because http.ServeContent needs it to size the response.
type blockingReader struct {
	size     int64
	pos      int64
	entered  chan struct{}
	once     sync.Once
	release  <-chan struct{}
	closedMu sync.Mutex
	closed   bool
}

func newBlockingReader(size int64, release <-chan struct{}) *blockingReader {
	return &blockingReader{size: size, entered: make(chan struct{}), release: release}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	if b.pos >= b.size {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > b.size-b.pos {
		n = int(b.size - b.pos)
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	b.pos += int64(n)
	return n, nil
}

func (b *blockingReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		b.pos = offset
	case io.SeekCurrent:
		b.pos += offset
	case io.SeekEnd:
		b.pos = b.size + offset
	}
	return b.pos, nil
}

func (b *blockingReader) Close() error {
	b.closedMu.Lock()
	defer b.closedMu.Unlock()
	b.closed = true
	return nil
}

func (b *blockingReader) isClosed() bool {
	b.closedMu.Lock()
	defer b.closedMu.Unlock()
	return b.closed
}

// nth returns the reader handed to the n'th request (1-based).
func (s *blockingSource) nth(n int) *blockingReader {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.readers) < n {
		return nil
	}
	return s.readers[n-1]
}

// blockingSource hands out blockingReaders, one per request, and reports when
// each has been entered.
type blockingSource struct {
	size    int64
	release chan struct{}

	mu      sync.Mutex
	readers []*blockingReader
}

func newBlockingSource(size int64) *blockingSource {
	return &blockingSource{size: size, release: make(chan struct{})}
}

func (s *blockingSource) Open(string) (io.ReadSeekCloser, int64, bool) {
	r := newBlockingReader(s.size, s.release)
	s.mu.Lock()
	s.readers = append(s.readers, r)
	s.mu.Unlock()
	return r, s.size, true
}

// waitInFlight blocks until the n'th request has reached its first Read, which
// is the point at which its seeding slot is definitely held.
func (s *blockingSource) waitInFlight(t *testing.T, n int) {
	t.Helper()
	for {
		s.mu.Lock()
		var r *blockingReader
		if len(s.readers) >= n {
			r = s.readers[n-1]
		}
		s.mu.Unlock()
		if r != nil {
			<-r.entered
			return
		}
	}
}

func (s *blockingSource) releaseAll() { close(s.release) }

// createIn makes a file in the stand-in cache. Separate from writePackage
// because the fuzz target has an *testing.F, not a *testing.T.
func createIn(dir, nameVersion string) (*os.File, error) {
	return os.Create(filepath.Join(dir, nameVersion))
}

func mustListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}
