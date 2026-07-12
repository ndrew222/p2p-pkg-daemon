package peer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/protocol"
)

// The bytes we got did not hash to the CID we asked for
var ErrHashMismatch = errors.New("fetch: downloaded bytes do not match the wanted CID")

// FetchFromPeer connects to one peer, asks for wantCID, reads the bytes back,
// and checks they hash to wantCID. On mismatch it discards the bytes and
// returns an error, so the caller can distrust that peer and try another
func FetchFromPeer(addr, wantCID string) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// 1. Ask the peer for the package
	req := protocol.Encode(protocol.Message{Type: protocol.MsgRequest, Payload: []byte(wantCID)})
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}

	// 2. Read the peer's reply off the connection
	reply, err := protocol.ReadMessage(conn)
	if err != nil {
		return nil, err
	}
	if reply.Type != protocol.MsgData {
		return nil, errors.New("fetch: peer did not send data")
	}

	// 3. Verify: hash the received bytes and compare to the CID we asked for
	sum := sha256.Sum256(reply.Payload)
	got := hex.EncodeToString(sum[:])
	if got != wantCID {
		return nil, ErrHashMismatch
	}
	return reply.Payload, nil
}
