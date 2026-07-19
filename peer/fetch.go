package peer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/peerwire"
)

const dialTimeout = 5 * time.Second

var (
	ErrHashMismatch = errors.New("peer: downloaded bytes do not match the requested CID")
	ErrNoPeers      = errors.New("peer: no peer could serve the CID")
	ErrPeerError    = errors.New("peer: remote returned an error")
	ErrBadCID       = errors.New("peer: malformed cid")
)

// validCID reports whether s is 64 lowercase hex chars (a SHA-256 CID)
func validCID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// FetchFromPeer connects to one peer, requests wantCID, reads the bytes back,
// and verifies they hash to wantCID
func FetchFromPeer(addr, wantCID string) ([]byte, error) {
	if !validCID(wantCID) {
		return nil, fmt.Errorf("peer: fetch: %w: %q", ErrBadCID, wantCID)
	}

	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("peer: fetch: dial %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))

	req := peerwire.Encode(peerwire.Message{Type: peerwire.MsgRequest, Payload: []byte(wantCID)})
	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("peer: fetch: write %s: %w", addr, err)
	}

	reply, err := peerwire.ReadMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("peer: fetch: read %s: %w", addr, err)
	}
	switch reply.Type {
	case peerwire.MsgData:
		// fall through to verify
	case peerwire.MsgError:
		return nil, fmt.Errorf("peer: fetch: %w: %s", ErrPeerError, reply.Payload)
	default:
		return nil, fmt.Errorf("peer: fetch: unexpected reply type %d from %s", reply.Type, addr)
	}

	// The received bytes must hash to the CID we asked for
	sum := sha256.Sum256(reply.Payload)
	if hex.EncodeToString(sum[:]) != wantCID {
		return nil, fmt.Errorf("peer: fetch from %s: %w", addr, ErrHashMismatch)
	}
	return reply.Payload, nil
}
