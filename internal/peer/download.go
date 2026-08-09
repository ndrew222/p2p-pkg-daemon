package peer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
)

// PeerLister returns the addresses of peers holding a package, keyed by
// name-version. In production this is the tracker client.
type PeerLister interface {
	Peers(nameVersion string) ([]string, error)
}

// FetchFirst runs UC-02's peer loop (steps 7-9) over addrs in the order the
// tracker returned them and returns the first verified copy as an OPEN,
// REWOUND temporary file. The caller owns it: Close and Remove it when done.
//
// The return type is a file rather than a byte slice for the same reason
// FetchFromPeer's is. This function is the one the mirror facade calls, so a
// []byte here would put the whole package back on the heap one layer above the
// wire and undo the streaming guarantee entirely.
//
// bl, which may be nil, is both read and written: blacklisted peers are
// skipped (§7) and a peer whose bytes fail verification is blacklisted before
// moving on (§11c). Any other failure -- unreachable, timeout, a non-200, a
// breach of the size bound -- costs one attempt and nothing else (§8e/§9e). In
// particular a size breach is NOT a blacklisting: the size is a bound, not a
// verdict.
//
// A local spool failure (ErrSpool) ends the loop immediately and is returned
// as-is. Trying the next peer would be pointless -- temp_dir is just as broken
// for them -- and would misattribute this daemon's fault to every holder in
// turn.
//
// It returns ErrNoPeers when addrs is empty, when every address was skipped,
// and when every attempt failed. Callers that need to tell those apart -- the
// mirror facade does, because "the tracker knows nobody" and "peers claimed it
// and failed to deliver" are different answers to pkg -- should inspect addrs
// themselves before calling.
func FetchFirst(ctx context.Context, addrs []string, nameVersion string, want Want, tempDir string, bl *Blacklist) (*os.File, error) {
	if !validName(nameVersion) {
		return nil, fmt.Errorf("peer: fetch: %w: %q", ErrBadName, nameVersion)
	}
	for _, addr := range addrs {
		if bl.Blocked(addr) {
			log.Printf("peer: skipping blacklisted peer %s for %q", addr, nameVersion)
			continue
		}
		file, err := FetchFromPeer(ctx, addr, nameVersion, want, tempDir)
		if err != nil {
			switch {
			case errors.Is(err, ErrSpool):
				// Ours, not theirs. Stop.
				return nil, err
			case errors.Is(err, ErrHashMismatch):
				// UC-02 §10c-11c: the bytes are already
				// discarded by FetchFromPeer; mark the peer
				// untrusted. Local only, never reported to the
				// tracker.
				//
				// The resulting list is reported because a
				// restart is the only thing that clears it --
				// there is no Unblock -- so the log is where an
				// operator sees what a restart would undo.
				bl.Block(addr)
				blocked := bl.Addrs()
				log.Printf("peer: blacklisted %s: corrupt bytes for %q; %d peer(s) blacklisted until restart: %s",
					addr, nameVersion, len(blocked), strings.Join(blocked, ", "))
			default:
				log.Printf("peer: fetch from %s failed: %v", addr, err)
			}
			continue
		}
		log.Printf("peer: fetched %q from %s (%d bytes, verified)", nameVersion, addr, want.Size)
		return file, nil
	}
	return nil, fmt.Errorf("peer: fetch: %w", ErrNoPeers)
}

// Download is the fetch entry point (UC-02): ask the lister who holds
// nameVersion, then try each peer until one returns bytes that verify against
// want. bl carries the local blacklist across calls and may be nil for a
// caller that keeps none.
//
// As with FetchFirst, the caller owns the returned file and must Close and
// Remove it.
func Download(ctx context.Context, lister PeerLister, nameVersion string, want Want, tempDir string, bl *Blacklist) (*os.File, error) {
	if !validName(nameVersion) {
		return nil, fmt.Errorf("peer: download: %w: %q", ErrBadName, nameVersion)
	}
	addrs, err := lister.Peers(nameVersion)
	if err != nil {
		return nil, fmt.Errorf("peer: download: %w", err)
	}
	file, err := FetchFirst(ctx, addrs, nameVersion, want, tempDir, bl)
	if err != nil {
		return nil, fmt.Errorf("peer: download: %w", err)
	}
	return file, nil
}

// Discard closes and removes a spool file returned by FetchFromPeer,
// FetchFirst or Download. It tolerates a nil file so a caller can defer it
// straight after the call.
//
// The buffer is per-request and ephemeral: the daemon has no store, and
// temp_dir is scratch, not a cache. Every path that takes a spool file must
// end here.
func Discard(f *os.File) {
	if f == nil {
		return
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		log.Printf("peer: removing spool file %s: %v", name, err)
	}
}
