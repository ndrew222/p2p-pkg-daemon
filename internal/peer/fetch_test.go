package peer

// The requester side of docs/peer-transfer-spec-v0.2.md's "Definition of done",
// cases 1-6. Case 7 (the fuzz target) is in fuzz_test.go; the seeder's own
// response codes and the two ADR-002 caps are in serve_test.go.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
)

// Case 1: a round trip. The seeder serves from a file, the requester verifies
// and returns it. This is the only test that proves the seeder, the wire, the
// size bound and hash verification all agree.
func TestRoundTrip(t *testing.T) {
	cache := t.TempDir()
	temp := t.TempDir()
	content := []byte("pretend this is a real .pkg archive")
	want := writePackage(t, cache, "nginx-1.24.0_2", content)

	addr := startSeeder(t, &Server{Source: dirSource(cache)})

	f, err := FetchFromPeer(context.Background(), addr, "nginx-1.24.0_2", want, temp)
	if err != nil {
		t.Fatalf("FetchFromPeer: %v", err)
	}
	defer Discard(f)

	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("body = %q, want %q", got, content)
	}

	// The file must arrive open and rewound: the caller streams from it
	// straight into its own response, so a caller forced to seek first
	// would be a caller that could forget to.
	if pos, err := f.Seek(0, io.SeekCurrent); err != nil || pos != want.Size {
		t.Errorf("after reading, offset = %d (err %v); the file should have been handed over rewound", pos, err)
	}

	// And it lives in temp_dir, not anywhere else.
	if dir := filepath.Dir(f.Name()); dir != temp {
		t.Errorf("spool landed in %q, want temp_dir %q", dir, temp)
	}
}

// Case 2: a package larger than the retired 64 MiB cap transfers, and neither
// end holds it in memory.
//
// The cap was not merely too small. It blocked 1.30% of the FreeBSD-ports
// repository outright -- llvm, rust, chromium, libreoffice -- which is
// precisely the set a P2P mirror exists to help with. Raising it would not
// have helped either, because both ends allocated the whole payload.
func TestPackageLargerThanTheRetiredCapTransfersWithoutResidency(t *testing.T) {
	if testing.Short() {
		t.Skip("writes and transfers ~64 MiB")
	}
	const size = 64<<20 + 4096 // one page past the constant that used to exist

	cache := t.TempDir()
	temp := t.TempDir()
	nameVersion := "texlive-docs-20240312"
	want := writeLargePackage(t, filepath.Join(cache, nameVersion), size)

	addr := startSeeder(t, &Server{Source: dirSource(cache)})

	// TotalAlloc is cumulative and unaffected by GC, so a whole-package
	// []byte anywhere in the path shows up here even if it is collected
	// immediately. The threshold is deliberately generous -- a quarter of
	// the file -- because the claim under test is "not resident", not "no
	// allocation at all", and any implementation that buffered the package
	// would exceed it several times over.
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	f, err := FetchFromPeer(context.Background(), addr, nameVersion, want, temp)
	if err != nil {
		t.Fatalf("FetchFromPeer: %v", err)
	}
	defer Discard(f)

	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > size/4 {
		t.Errorf("transfer allocated %d bytes for a %d-byte package; neither end may hold it in memory", allocated, size)
	}

	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Errorf("spooled %d bytes, want %d", info.Size(), size)
	}
}

// The structural half of case 2, and the one that survives a machine too fast
// or too slow for an allocation threshold to mean anything: a []byte in either
// signature is the regression, so assert that neither has one.
func TestNoByteSliceInTheTransferSignatures(t *testing.T) {
	check := func(name string, fn reflect.Type) {
		t.Helper()
		byteSlice := reflect.TypeOf([]byte(nil))
		for i := 0; i < fn.NumIn(); i++ {
			if fn.In(i) == byteSlice {
				t.Errorf("%s takes a []byte at parameter %d; the transfer path must be constant-memory", name, i)
			}
		}
		for i := 0; i < fn.NumOut(); i++ {
			if fn.Out(i) == byteSlice {
				t.Errorf("%s returns a []byte at result %d; the transfer path must be constant-memory", name, i)
			}
		}
	}

	check("FetchFromPeer", reflect.TypeOf(FetchFromPeer))
	check("FetchFirst", reflect.TypeOf(FetchFirst))
	check("Download", reflect.TypeOf(Download))

	src := reflect.TypeOf((*PackageSource)(nil)).Elem()
	open, ok := src.MethodByName("Open")
	if !ok {
		t.Fatal("PackageSource has no Open method")
	}
	check("PackageSource.Open", open.Type)

	// And the positive statement: Open hands back a handle.
	if got := open.Type.Out(0); got != reflect.TypeOf((*io.ReadSeekCloser)(nil)).Elem() {
		t.Errorf("PackageSource.Open returns %s, want io.ReadSeekCloser", got)
	}
}

// Cases 3, 4 and 5: everything a peer can get wrong about the bytes, and what
// each one costs it.
//
// The distinction that matters here is which failures are a VERDICT about the
// peer and which are merely a bound. Only a hash mismatch blacklists (UC-02
// §11c). A size breach abandons the peer and moves on, because a body of the
// wrong length fails the hash anyway if read to completion, so a separate size
// verdict would be a second route to the same conclusion.
func TestRequesterRejections(t *testing.T) {
	content := []byte("the genuine package bytes")
	good := Want{Hash: hashOf(content), Size: int64(len(content))}

	tests := []struct {
		name            string
		handler         http.HandlerFunc
		want            Want
		wantErr         error
		wantBlacklisted bool
		// wantRejectedBeforeTheBody asserts the peer never got to send
		// a byte. Proved structurally: the fetch is repeated against a
		// temp_dir that does not exist, and a run that had started
		// spooling would fail with ErrSpool instead.
		wantRejectedBeforeTheBody bool
	}{
		{
			// Case 3.
			name: "wrong bytes fail verification and blacklist the peer",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(content)))
				w.Write([]byte("TAMPERED with!!!!!!!!!!!!"))
			},
			want:            good,
			wantErr:         ErrHashMismatch,
			wantBlacklisted: true,
		},
		{
			// Case 4: the body runs past the expected size. The
			// LimitReader lets exactly one byte through beyond it,
			// which is what turns "too long" into a detectable
			// condition rather than an unbounded read.
			name: "a body longer than the expected size is cut off and rejected",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// No Content-Length: chunked, so the cheap
				// check cannot fire and the LimitReader is
				// what has to hold.
				w.Header().Set("Transfer-Encoding", "chunked")
				for i := 0; i < 64; i++ {
					w.Write(content)
				}
			},
			want:    good,
			wantErr: ErrSizeMismatch,
		},
		{
			name: "a body shorter than the expected size is rejected",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Transfer-Encoding", "chunked")
				w.Write(content[:5])
			},
			want:    good,
			wantErr: ErrSizeMismatch,
		},
		{
			// Case 5. The peer never gets to send a byte: the
			// declared length already contradicts the repository
			// database, so there is nothing to learn from reading
			// it and a great deal to lose.
			name: "a Content-Length disagreeing with the expected size is rejected without reading the body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.FormatInt(good.Size*1000, 10))
				w.WriteHeader(http.StatusOK)
			},
			want:                      good,
			wantErr:                   ErrSizeMismatch,
			wantRejectedBeforeTheBody: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			temp := t.TempDir()
			addr := startRawPeer(t, tc.handler)

			var bl Blacklist
			f, err := FetchFirst(context.Background(), []string{addr}, "nginx-1.24.0_2", tc.want, temp, &bl)
			if f != nil {
				Discard(f)
				t.Fatal("a rejected transfer must not yield a file")
			}
			// FetchFirst collapses every peer failure into
			// ErrNoPeers, which is the contract the facade relies
			// on; the specific cause is asserted through
			// FetchFromPeer below.
			if !errors.Is(err, ErrNoPeers) {
				t.Fatalf("FetchFirst = %v, want ErrNoPeers", err)
			}

			_, single := FetchFromPeer(context.Background(), addr, "nginx-1.24.0_2", tc.want, temp)
			if !errors.Is(single, tc.wantErr) {
				t.Fatalf("FetchFromPeer = %v, want %v", single, tc.wantErr)
			}

			assertTempDirEmpty(t, temp)

			if got := bl.Blocked(addr); got != tc.wantBlacklisted {
				t.Errorf("blacklisted = %v, want %v; only a hash mismatch is a verdict about the peer", got, tc.wantBlacklisted)
			}

			if !tc.wantRejectedBeforeTheBody {
				return
			}
			// Nothing may be spooled, because nothing may be read.
			// A temp_dir that cannot be written to therefore makes
			// no difference: reaching it at all would turn this
			// into ErrSpool.
			_, err = FetchFromPeer(context.Background(), addr, "nginx-1.24.0_2", tc.want,
				filepath.Join(t.TempDir(), "does-not-exist"))
			if errors.Is(err, ErrSpool) {
				t.Error("the body was spooled before the Content-Length check; it must be rejected without reading a byte")
			}
			if !errors.Is(err, ErrSizeMismatch) {
				t.Errorf("FetchFromPeer = %v, want ErrSizeMismatch", err)
			}
		})
	}
}

// Case 6: every non-200 advances the requester to the next peer, and none of
// them is a verdict about the peer that sent it.
//
// 503 in particular: it means the peer holds the package and is refusing to
// serve it right now, so treating it as evidence of anything would evict
// honest, busy peers from the swarm.
func TestEveryNon200AdvancesToTheNextPeer(t *testing.T) {
	content := []byte("the genuine package bytes")
	want := Want{Hash: hashOf(content), Size: int64(len(content))}

	for _, code := range []int{
		http.StatusNotFound,
		http.StatusBadRequest,
		http.StatusMethodNotAllowed,
		http.StatusServiceUnavailable,
		http.StatusInternalServerError,
		http.StatusMovedPermanently,
	} {
		t.Run(fmt.Sprintf("%d", code), func(t *testing.T) {
			temp := t.TempDir()
			var hits int
			bad := startRawPeer(t, func(w http.ResponseWriter, r *http.Request) {
				hits++
				http.Error(w, "no", code)
			})
			cache := t.TempDir()
			writePackage(t, cache, "nginx-1.24.0_2", content)
			good := startSeeder(t, &Server{Source: dirSource(cache)})

			var bl Blacklist
			f, err := FetchFirst(context.Background(), []string{bad, good}, "nginx-1.24.0_2", want, temp, &bl)
			if err != nil {
				t.Fatalf("FetchFirst: %v", err)
			}
			defer Discard(f)

			got, _ := io.ReadAll(f)
			if string(got) != string(content) {
				t.Errorf("body = %q, want %q", got, content)
			}
			if hits == 0 {
				t.Error("the first peer was never contacted")
			}
			if bl.Len() != 0 {
				t.Errorf("a %d blacklisted %v; only a hash mismatch is a verdict", code, bl.Addrs())
			}
		})
	}
}

// A dial failure is an ordinary network condition (UC-02 §8e/§9e): move on,
// but do not mark the peer untrusted. Blacklisting on it would evict half a
// swarm after one flaky minute.
func TestUnreachablePeerIsNotBlacklisted(t *testing.T) {
	content := []byte("bytes")
	want := Want{Hash: hashOf(content), Size: int64(len(content))}
	cache := t.TempDir()
	writePackage(t, cache, "curl-8.6.0", content)

	var bl Blacklist
	f, err := FetchFirst(context.Background(),
		[]string{"127.0.0.1:1", startSeeder(t, &Server{Source: dirSource(cache)})},
		"curl-8.6.0", want, t.TempDir(), &bl)
	if err != nil {
		t.Fatalf("FetchFirst: %v", err)
	}
	Discard(f)
	if bl.Len() != 0 {
		t.Errorf("unreachable peer was blacklisted: %v", bl.Addrs())
	}
}

// An unwritable temp_dir is THIS daemon's fault, not the swarm's. Trying the
// next peer would be pointless -- temp_dir is just as broken for them -- and
// would misattribute a local failure to every holder in turn, which is how an
// operator ends up debugging the network instead of the disk.
func TestSpoolFailureStopsTheLoopAndIsDistinguishable(t *testing.T) {
	content := []byte("bytes")
	want := Want{Hash: hashOf(content), Size: int64(len(content))}
	cache := t.TempDir()
	writePackage(t, cache, "jq-1.7", content)

	var hits int
	counted := startRawPeer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write(content)
	})
	second := startSeeder(t, &Server{Source: dirSource(cache)})

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := FetchFirst(context.Background(), []string{counted, second}, "jq-1.7", want, missing, nil)
	if !errors.Is(err, ErrSpool) {
		t.Fatalf("FetchFirst = %v, want ErrSpool", err)
	}
	if hits != 1 {
		t.Errorf("first peer contacted %d times, want 1", hits)
	}
}

// A blacklisted peer is skipped at selection, not contacted and discarded
// (UC-02 §7). The distinction is observable: the whole point is to stop paying
// for a known-bad peer on every later request.
func TestBlacklistedPeerIsSkippedAtSelection(t *testing.T) {
	content := []byte("bytes")
	want := Want{Hash: hashOf(content), Size: int64(len(content))}

	var hits int
	addr := startRawPeer(t, func(w http.ResponseWriter, r *http.Request) { hits++ })

	var bl Blacklist
	bl.Block(addr)
	if _, err := FetchFirst(context.Background(), []string{addr}, "jq-1.7", want, t.TempDir(), &bl); !errors.Is(err, ErrNoPeers) {
		t.Fatalf("FetchFirst = %v, want ErrNoPeers when every peer is blacklisted", err)
	}
	if hits != 0 {
		t.Errorf("blacklisted peer was contacted %d times", hits)
	}
}

// Download is the entry point the facade's predecessor used: ask the lister,
// then run the loop.
func TestDownloadThroughLister(t *testing.T) {
	content := []byte("package via download")
	cache := t.TempDir()
	want := writePackage(t, cache, "curl-8.6.0", content)
	addr := startSeeder(t, &Server{Source: dirSource(cache)})

	f, err := Download(context.Background(), stubLister{addr: addr}, "curl-8.6.0", want, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer Discard(f)
	got, _ := io.ReadAll(f)
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

type stubLister struct{ addr string }

func (s stubLister) Peers(string) ([]string, error) { return []string{s.addr}, nil }

// A cancelled request must not leave a spool file behind. pkg hanging up
// mid-install is the ordinary way this happens.
func TestCancelledFetchLeavesNothingBehind(t *testing.T) {
	temp := t.TempDir()
	src := newBlockingSource(1 << 20)
	addr := startSeeder(t, &Server{Source: src})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := FetchFromPeer(ctx, addr, "nginx-1.24.0_2", Want{Hash: hashOf(nil), Size: 1 << 20}, temp)
		done <- err
	}()
	src.waitInFlight(t, 1)
	cancel()

	if err := <-done; err == nil {
		t.Fatal("a cancelled fetch returned no error")
	}
	src.releaseAll()
	assertTempDirEmpty(t, temp)
}

func TestFetchRejectsAnInvalidNameVersion(t *testing.T) {
	for _, nv := range []string{"", "with\x00nul", "with\nnewline"} {
		if _, err := FetchFromPeer(context.Background(), "127.0.0.1:1", nv, Want{}, os.TempDir()); !errors.Is(err, ErrBadName) {
			t.Errorf("FetchFromPeer(%q) = %v, want ErrBadName", nv, err)
		}
	}
}

// writeLargePackage writes size bytes without ever holding them, and returns
// the Want the repository database would carry for the result. A test that
// built the content as one []byte would be asserting constant memory from a
// harness that had already broken it.
func writeLargePackage(t *testing.T, path string, size int64) Want {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	sum := newHasher()
	var written int64
	for written < size {
		n := int64(len(chunk))
		if remaining := size - written; remaining < n {
			n = remaining
		}
		if _, err := f.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		sum.Write(chunk[:n])
		written += n
	}
	return Want{Hash: sum.hex(), Size: size}
}
