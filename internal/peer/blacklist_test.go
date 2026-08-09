package peer

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
)

func TestBlacklistNilReceiverIsInert(t *testing.T) {
	var bl *Blacklist
	bl.Block("127.0.0.1:1") // must not panic
	if bl.Blocked("127.0.0.1:1") {
		t.Fatal("a nil blacklist blocks nothing")
	}
	if bl.Len() != 0 || bl.Addrs() != nil {
		t.Fatal("a nil blacklist is empty")
	}
}

func TestBlacklistZeroValueAndConcurrency(t *testing.T) {
	var bl Blacklist
	if bl.Blocked("127.0.0.1:1") {
		t.Fatal("nothing is blocked before anything is blocked")
	}

	var wg sync.WaitGroup
	for _, addr := range []string{"a:1", "b:2", "a:1", "c:3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bl.Block(addr)
			bl.Blocked(addr)
		}()
	}
	wg.Wait()

	if got := bl.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3 (re-blocking is a no-op)", got)
	}
	want := []string{"a:1", "b:2", "c:3"}
	got := bl.Addrs()
	if len(got) != len(want) {
		t.Fatalf("Addrs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Addrs = %v, want %v", got, want)
		}
	}
}

// servePeer answers every request with content and counts how many it got. It
// is deliberately not a real Server: the point of these tests is what the
// FETCH LOOP does with a peer, so the peer is reduced to "always answers this".
func servePeer(t *testing.T, content []byte) (addr string, requests func() int) {
	t.Helper()
	var mu sync.Mutex
	var n int
	addr = startRawPeer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write(content)
	})
	return addr, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// UC-02 §11c: a peer serving bytes that fail verification is marked untrusted,
// and §7: later selections skip it.
func TestFetchFirstBlacklistsCorruptPeer(t *testing.T) {
	content := []byte("the genuine bytes")
	// Same length, different bytes: the size bound passes and the hash is
	// what catches it, which is the case the blacklist is actually for.
	want := Want{Hash: hashOf(content), Size: int64(len(content))}

	corrupt, corruptRequests := servePeer(t, []byte("TAMPERED  bytes!!"))
	good, _ := servePeer(t, content)

	var bl Blacklist

	f, err := FetchFirst(context.Background(), []string{corrupt, good}, "nginx-1.24.0_2", want, t.TempDir(), &bl)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, _ := io.ReadAll(f)
	Discard(f)
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
	if !bl.Blocked(corrupt) {
		t.Fatal("the hash-mismatching peer was not blacklisted")
	}
	if bl.Blocked(good) {
		t.Fatal("the verifying peer must not be blacklisted")
	}

	// Second fetch: the corrupt peer is skipped, not retried.
	before := corruptRequests()
	f, err = FetchFirst(context.Background(), []string{corrupt, good}, "nginx-1.24.0_2", want, t.TempDir(), &bl)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	Discard(f)
	if after := corruptRequests(); after != before {
		t.Fatalf("blacklisted peer was contacted again (%d -> %d requests)", before, after)
	}
}
