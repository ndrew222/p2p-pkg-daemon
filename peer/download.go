package peer

import (
	"fmt"
	"log"
)

// PeerLister returns the addresses of peers holding a CID. For now it returns
// plain "host:port" strings so this package compiles on its own
type PeerLister interface {
	Peers(cid string) ([]string, error)
}

// Download is the fetch entry point (UC-02 / UC-04): ask the lister who holds
// cid, then try each peer until one returns bytes that verify
func Download(lister PeerLister, cid string) ([]byte, error) {
	if !validCID(cid) {
		return nil, fmt.Errorf("peer: download: %w: %q", ErrBadCID, cid)
	}
	addrs, err := lister.Peers(cid)
	if err != nil {
		return nil, fmt.Errorf("peer: download: %w", err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("peer: download: %w", ErrNoPeers)
	}
	for _, addr := range addrs {
		data, err := FetchFromPeer(addr, cid)
		if err != nil {
			log.Printf("peer: fetch from %s failed: %v", addr, err)
			continue // dont trust this holder, try the next
		}
		log.Printf("peer: fetched cid=%q from %s (%d bytes)", cid, addr, len(data))
		return data, nil
	}
	return nil, fmt.Errorf("peer: download: %w", ErrNoPeers)
}
