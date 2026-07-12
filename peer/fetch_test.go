package peer

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"testing"

	"github.com/ndrew222/p2p-pkg-daemon/protocol"
)

// startTestPeer spins up a fake peer that always serves `content`, so we can
// test FetchFromPeer without needing a real second machine
func startTestPeer(t *testing.T, content []byte) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // :0 = OS picks a free port
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
		if _, err := protocol.ReadMessage(conn); err != nil { // read the request
			return
		}
		conn.Write(protocol.Encode(protocol.Message{Type: protocol.MsgData, Payload: content}))
	}()
	return ln.Addr().String()
}

func TestFetchFromPeerHappyPath(t *testing.T) {
	content := []byte("pretend package bytes")
	sum := sha256.Sum256(content)
	cid := hex.EncodeToString(sum[:])

	addr := startTestPeer(t, content)

	got, err := FetchFromPeer(addr, cid)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("got %q, want %q", got, content)
	}
}

func TestFetchRejectsTamperedBytes(t *testing.T) {
	// We ask for the CID of the RIGHT bytes, but the peer serves WRONG bytes
	sum := sha256.Sum256([]byte("right bytes"))
	wantCID := hex.EncodeToString(sum[:])

	addr := startTestPeer(t, []byte("wrong bytes"))

	if _, err := FetchFromPeer(addr, wantCID); err == nil {
		t.Fatal("expected a hash-mismatch error, got nil")
	}
}
