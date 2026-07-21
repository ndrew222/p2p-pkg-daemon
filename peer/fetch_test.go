package peer

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"testing"

	"github.com/ndrew222/p2p-pkg-daemon/peerwire"
)

// hashOf stands in for the expected hash the daemon would read from pkg's repo DB.
func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func startTestPeer(t *testing.T, content []byte) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := peerwire.ReadMessage(conn); err != nil {
			return
		}
		conn.Write(peerwire.Encode(peerwire.Message{Type: peerwire.MsgData, Payload: content}))
	}()
	return ln.Addr().String()
}

func TestFetchFromPeerHappyPath(t *testing.T) {
	content := []byte("pretend nginx-1.24.0_2 package bytes")
	addr := startTestPeer(t, content)
	got, err := FetchFromPeer(addr, "nginx-1.24.0_2", hashOf(content))
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

func TestFetchRejectsTamperedBytes(t *testing.T) {
	// Expected hash is of the RIGHT bytes; peer serves WRONG bytes.
	wantHash := hashOf([]byte("right bytes"))
	addr := startTestPeer(t, []byte("WRONG bytes"))
	if _, err := FetchFromPeer(addr, "nginx-1.24.0_2", wantHash); err == nil {
		t.Fatal("expected a hash-mismatch error, got nil")
	}
}

type stubLister struct{ addr string }

func (s stubLister) Peers(nameVersion string) ([]string, error) { return []string{s.addr}, nil }

func TestDownloadThroughLister(t *testing.T) {
	content := []byte("package via download")
	addr := startTestPeer(t, content)
	got, err := Download(stubLister{addr: addr}, "curl-8.6.0", hashOf(content))
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}
