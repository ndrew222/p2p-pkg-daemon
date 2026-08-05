package peer

import (
	"sort"
	"sync"
)

// Blacklist is the local list of peers that have served bytes failing hash
// verification (UC-02 §11c). It is consulted at peer selection (UC-02 §7,
// "skipping any on its local blacklist") and written on a hash mismatch.
//
// Local only: nothing here is ever reported to the tracker, and the tracker is
// never asked about it. A peer that sends corrupt bytes is this daemon's
// problem and this daemon's judgement.
//
// Only a hash mismatch blacklists a peer. An unreachable peer or a timeout
// (UC-02 §8e/§9e) says "move on to the next peer" and nothing more -- those are
// ordinary network conditions, not evidence of a bad actor, and blacklisting on
// them would evict half a swarm after one flaky minute.
//
// RATIFIED by the owner (see the work log). The three positions below were
// left undecided by the use case and are now decided as the code already had
// them:
//
//   - The key is the **peer address**, so a peer that serves corrupt bytes is
//     distrusted for everything, not for the one package it lied about.
//     Corrupt bytes are evidence about the peer, not about the file.
//   - No expiry. The use case says "mark the peer untrusted"; it does not say
//     entries ever lapse, and any TTL would be a number nobody chose. Entries
//     therefore live as long as the process.
//   - No persistence. The daemon holds write permission on its buffer
//     directory and config path only (UC-01 assumptions), so a blacklist file
//     would need a storage decision that does not exist. The list starts empty
//     on every start.
//
// Culling is by restart, and that is the whole mechanism: the list is
// in-memory and unpersisted, so a restart clears it completely. There is no
// Unblock and none is planned -- something would have to call it, an admin
// surface is in no spec, and inventing one would trip AGENTS.md ground rule 2.
// What the ruling does require is that the log show what a restart would
// clear, which is why Download reports the resulting list on every Block.
//
// The zero value is ready to use. All methods are safe for concurrent use and
// tolerate a nil receiver, so a caller with no blacklist can pass nil and get
// no blacklisting rather than a panic.
type Blacklist struct {
	mu      sync.RWMutex
	blocked map[string]struct{}
}

// Block marks addr untrusted. Blocking an already-blocked address is a no-op.
func (b *Blacklist) Block(addr string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked == nil {
		b.blocked = make(map[string]struct{})
	}
	b.blocked[addr] = struct{}{}
}

// Blocked reports whether addr has been marked untrusted.
func (b *Blacklist) Blocked(addr string) bool {
	if b == nil {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.blocked[addr]
	return ok
}

// Len returns the number of blacklisted addresses.
func (b *Blacklist) Len() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.blocked)
}

// Addrs returns the blacklisted addresses in sorted order. Used by the fetch
// loop's log line to report what a restart would clear, and by tests; the
// selection path itself uses Blocked.
func (b *Blacklist) Addrs() []string {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.blocked) == 0 {
		return nil
	}
	addrs := make([]string, 0, len(b.blocked))
	for addr := range b.blocked {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	return addrs
}
