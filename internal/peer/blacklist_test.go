package peer

import (
	"net"
	"sync"
	"testing"

	"github.com/ndrew222/p2p-pkg-daemon/internal/peerwire"
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

// servePeer answers every connection with content, forever. Unlike
// startTestPeer it survives more than one request, which is what the
// blacklist tests need.
func servePeer(t *testing.T, content []byte) (addr string, requests func() int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	var n int
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			n++
			mu.Unlock()
			go func() {
				defer conn.Close()
				if _, err := peerwire.ReadMessage(conn); err != nil {
					return
				}
				conn.Write(peerwire.Encode(peerwire.Message{Type: peerwire.MsgData, Payload: content}))
			}()
		}
	}()
	return ln.Addr().String(), func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// UC-02 §11c: a peer serving bytes that fail verification is marked untrusted,
// and §7: later selections skip it.
func TestFetchFirstBlacklistsCorruptPeer(t *testing.T) {
	content := []byte("the genuine bytes")
	wantHash := hashOf(content)

	corrupt, corruptRequests := servePeer(t, []byte("TAMPERED bytes"))
	good, _ := servePeer(t, content)

	var bl Blacklist

	got, err := FetchFirst([]string{corrupt, good}, "nginx-1.24.0_2", wantHash, &bl)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
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
	if _, err := FetchFirst([]string{corrupt, good}, "nginx-1.24.0_2", wantHash, &bl); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if after := corruptRequests(); after != before {
		t.Fatalf("blacklisted peer was contacted again (%d -> %d requests)", before, after)
	}
}

// An unreachable peer is an ordinary network condition (UC-02 §8e/§9e): move on
// to the next peer, but do not mark it untrusted.
func TestFetchFirstDoesNotBlacklistUnreachablePeer(t *testing.T) {
	content := []byte("still the genuine bytes")

	// A listener closed immediately gives an address nobody is serving.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()

	good, _ := servePeer(t, content)

	var bl Blacklist
	if _, err := FetchFirst([]string{dead, good}, "curl-8.6.0", hashOf(content), &bl); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if bl.Len() != 0 {
		t.Fatalf("unreachable peer was blacklisted: %v", bl.Addrs())
	}
}

// Every candidate blacklisted is indistinguishable from every candidate failing:
// both are ErrNoPeers, and neither contacts anyone.
func TestFetchFirstAllBlacklisted(t *testing.T) {
	content := []byte("bytes")
	addr, requests := servePeer(t, content)

	var bl Blacklist
	bl.Block(addr)

	if _, err := FetchFirst([]string{addr}, "jq-1.7", hashOf(content), &bl); err == nil {
		t.Fatal("expected ErrNoPeers when every peer is blacklisted")
	}
	if n := requests(); n != 0 {
		t.Fatalf("blacklisted peer was contacted %d times", n)
	}
}
