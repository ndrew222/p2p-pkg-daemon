package peer

import (
	"errors"
	"fmt"
	"log"
	"strings"
)

// PeerLister returns the addresses of peers holding a package. It takes a name-version
type PeerLister interface {
	Peers(nameVersion string) ([]string, error)
}

// FetchFirst runs UC-02's peer loop (steps 7-9) over addrs in the order the
// tracker returned them and returns the bytes of the first peer whose reply
// verifies against expectedHash.
//
// bl, which may be nil, is both read and written: blacklisted peers are skipped
// (§7) and a peer whose bytes fail verification is blacklisted before moving on
// (§11c). Any other failure -- unreachable, timeout, peer-side error -- costs
// one attempt and nothing else (§8e/§9e).
//
// It returns ErrNoPeers when addrs is empty, when every address was skipped,
// and when every attempt failed. Callers that need to tell those apart --
// the mirror facade does, because "the tracker knows nobody" and "peers claimed
// it and failed to deliver" are different answers to pkg -- should inspect
// addrs themselves before calling.
func FetchFirst(addrs []string, nameVersion, expectedHash string, bl *Blacklist) ([]byte, error) {
	if !validName(nameVersion) {
		return nil, fmt.Errorf("peer: fetch: %w: %q", ErrBadName, nameVersion)
	}
	for _, addr := range addrs {
		if bl.Blocked(addr) {
			log.Printf("peer: skipping blacklisted peer %s for %q", addr, nameVersion)
			continue
		}
		data, err := FetchFromPeer(addr, nameVersion, expectedHash)
		if err != nil {
			if errors.Is(err, ErrHashMismatch) {
				// UC-02 §10c-11c: discard the bytes (returning nil does that)
				// and mark the peer untrusted. Local only.
				//
				// The resulting list is reported because a restart is the
				// only thing that clears it -- there is no Unblock -- so
				// the log is where an operator sees what a restart would
				// undo.
				bl.Block(addr)
				blocked := bl.Addrs()
				log.Printf("peer: blacklisted %s: corrupt bytes for %q; %d peer(s) blacklisted until restart: %s",
					addr, nameVersion, len(blocked), strings.Join(blocked, ", "))
			} else {
				log.Printf("peer: fetch from %s failed: %v", addr, err)
			}
			continue
		}
		log.Printf("peer: fetched %q from %s (%d bytes)", nameVersion, addr, len(data))
		return data, nil
	}
	return nil, fmt.Errorf("peer: fetch: %w", ErrNoPeers)
}

// Download is the fetch entry point (UC-02): ask the lister who holds
// nameVersion, then try each peer until one returns bytes that verify against
// expectedHash. bl carries the local blacklist across calls and may be nil for
// a caller that keeps none.
func Download(lister PeerLister, nameVersion, expectedHash string, bl *Blacklist) ([]byte, error) {
	if !validName(nameVersion) {
		return nil, fmt.Errorf("peer: download: %w: %q", ErrBadName, nameVersion)
	}
	addrs, err := lister.Peers(nameVersion)
	if err != nil {
		return nil, fmt.Errorf("peer: download: %w", err)
	}
	data, err := FetchFirst(addrs, nameVersion, expectedHash, bl)
	if err != nil {
		return nil, fmt.Errorf("peer: download: %w", err)
	}
	return data, nil
}
