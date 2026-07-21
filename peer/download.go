package peer

import (
	"fmt"
	"log"
)

// PeerLister returns the addresses of peers holding a package. It takes a name-version
type PeerLister interface {
	Peers(nameVersion string) ([]string, error)
}

// Download is the fetch entry point (UC-02): ask the lister who holds
// nameVersion, then try each peer (up to the tracker's cap of 3) until one
// returns bytes that verify against expectedHash.
func Download(lister PeerLister, nameVersion, expectedHash string) ([]byte, error) {
	if !validName(nameVersion) {
		return nil, fmt.Errorf("peer: download: %w: %q", ErrBadName, nameVersion)
	}
	addrs, err := lister.Peers(nameVersion)
	if err != nil {
		return nil, fmt.Errorf("peer: download: %w", err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("peer: download: %w", ErrNoPeers)
	}
	for _, addr := range addrs {
		data, err := FetchFromPeer(addr, nameVersion, expectedHash)
		if err != nil {
			log.Printf("peer: fetch from %s failed: %v", addr, err)
			continue
		}
		log.Printf("peer: fetched %q from %s (%d bytes)", nameVersion, addr, len(data))
		return data, nil
	}
	return nil, fmt.Errorf("peer: download: %w", ErrNoPeers)
}
