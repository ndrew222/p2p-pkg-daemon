// Package peer is the fetch-and-seed daemon. Packages are addressed by
// name-version strings. Integrity is verified against an expected hash from pkg's repository DB, passed in
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
	ErrHashMismatch = errors.New("peer: downloaded bytes do not match the expected hash")
	ErrNoPeers      = errors.New("peer: no peer could serve the package")
	ErrPeerError    = errors.New("peer: remote returned an error")
	ErrBadName      = errors.New("peer: invalid package name-version")
)

// validName is a MINIMAL sanity check. The exact name-version format is not
// specified in the docs (example: "nginx-1.24.0_2"), so this only rejects
// empty / oversized / control-character input. TODO: tighten once the spec
// defines the pattern -- do not invent a stricter rule here
func validName(s string) bool {
	if len(s) == 0 || len(s) > 255 {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// FetchFromPeer requests nameVersion from one peer, reads the bytes, and
// verifies them against expectedHash. name-version identifies the package; the
// hash proves integrity. On mismatch the bytes are discarded so the caller
// can try another peer.
//
// ASSUMPTION: expectedHash is the lowercase hex
// SHA-256 of the package file. The repo DB's exact hash format is not in the
// docs; sha256-hex is the working assumption and is isolated to this one spot.
func FetchFromPeer(addr, nameVersion, expectedHash string) ([]byte, error) {
	if !validName(nameVersion) {
		return nil, fmt.Errorf("peer: fetch: %w: %q", ErrBadName, nameVersion)
	}

	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("peer: fetch: dial %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))

	req := peerwire.Encode(peerwire.Message{Type: peerwire.MsgRequest, Payload: []byte(nameVersion)})
	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("peer: fetch: write %s: %w", addr, err)
	}

	reply, err := peerwire.ReadMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("peer: fetch: read %s: %w", addr, err)
	}
	switch reply.Type {
	case peerwire.MsgData:
		// verify below
	case peerwire.MsgError:
		return nil, fmt.Errorf("peer: fetch: %w: %s", ErrPeerError, reply.Payload)
	default:
		return nil, fmt.Errorf("peer: fetch: unexpected reply type %d from %s", reply.Type, addr)
	}

	sum := sha256.Sum256(reply.Payload)
	if hex.EncodeToString(sum[:]) != expectedHash {
		return nil, fmt.Errorf("peer: fetch from %s: %w", addr, ErrHashMismatch)
	}
	return reply.Payload, nil
}
