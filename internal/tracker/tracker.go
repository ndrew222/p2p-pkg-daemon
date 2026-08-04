// Package tracker is the in-memory lookup service of tracker protocol v0.2.
// Daemons announce the packages they can serve; it answers "who has package
// X?" with addresses. It never relays package bytes and never verifies that a
// daemon really holds what it announced -- integrity is end-to-end, checked by
// the downloading daemon against a trusted hash.
package tracker

import (
	"log"
	"sync"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
)

const (
	// Timeout is v0.2 TIMEOUT: how long a registration survives without a
	// ping or announce. Config-overridable via NewWithTimeout; the only
	// hard rule in the spec is PING_INTERVAL < TIMEOUT (20s < 60s).
	Timeout = 60 * time.Second

	// SweepInterval is how often expired entries are reaped. Not a
	// protocol constant -- Peers() already ignores entries past their
	// deadline, so the sweeper is only there to stop the maps growing.
	SweepInterval = 15 * time.Second

	// MaxPeers is v0.2 MAX_PEERS: the cap on a /peers reply. Provisional,
	// pending the unresolved 3-vs-1 privacy question from v0.1.
	MaxPeers = 3
)

// peerRecord is one daemon's registration. Keyed by IP in Tracker.peers, so
// the IP is not repeated here.
type peerRecord struct {
	// ServingPort is the peer-transfer listen port. It must come from the
	// announce body: the source port of the daemon's outbound HTTP
	// connection is unrelated to what it listens on.
	ServingPort int

	// Deadline is when this entry dies if nothing refreshes it. The spec
	// models expiry as a deadline rather than a last-seen timestamp, so
	// that is what is stored.
	Deadline time.Time

	// Go has no set type. A map to the empty struct is the cheap stand-in:
	// zero bytes per entry.
	Packages map[string]struct{}
}

// Tracker is the in-memory registry. Safe for concurrent use.
//
// v0.2 §State keys entries by public IP, taken from the connection's source
// address and never from a message body. One consequence, accepted for now:
// one daemon per public IP. Two daemons behind the same NAT overwrite each
// other's entry.
type Tracker struct {
	mu sync.RWMutex

	// peers is IP -> record. Pointer values, so a ping refreshes the record
	// in the map rather than a copy of it.
	peers map[string]*peerRecord

	// packages is the reverse index, name-version -> set of IPs. Kept in
	// step with peers under the same lock; Peers() would otherwise have to
	// scan every registration on every lookup.
	packages map[string]map[string]struct{}

	// timeout is Timeout unless overridden. Held per-tracker so tests can
	// exercise expiry without waiting a minute.
	timeout time.Duration
}

// New returns an empty Tracker with the spec default timeout.
func New() *Tracker {
	return NewWithTimeout(Timeout)
}

// NewWithTimeout returns an empty Tracker with a custom entry timeout.
// Maps must be made before use; writing to a nil map panics.
func NewWithTimeout(timeout time.Duration) *Tracker {
	return &Tracker{
		peers:    make(map[string]*peerRecord),
		packages: make(map[string]map[string]struct{}),
		timeout:  timeout,
	}
}

// Announce registers or refreshes the daemon at ip and replaces its package
// list. The list is a FULL REPLACEMENT, never a delta.
//
// An empty list deregisters: reply 200, store nothing, delete any existing
// entry. That is how a daemon that just ran `pkg clean` withdraws, and how a
// fresh daemon confirms the tracker is reachable.
//
// Accepted from any IP, known or unknown, solicited (after a 404 ping) or
// unprompted. The caller is responsible for having validated req -- the
// handler does that so a malformed body becomes a 400 rather than reaching
// here at all.
func (t *Tracker) Announce(ip string, req *proto.AnnounceRequest) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Strip the old claims first either way. A peer that dropped a package
	// must stop being listed for it, or the index only ever grows.
	t.removeFromIndex(ip)

	if len(req.Packages) == 0 {
		delete(t.peers, ip)
		log.Printf("tracker: announce ip=%s packages=0 (deregistered)", ip)
		return
	}

	set := make(map[string]struct{}, len(req.Packages))
	for _, nameVersion := range req.Packages {
		set[nameVersion] = struct{}{}

		holders, ok := t.packages[nameVersion]
		if !ok {
			// Absent key means a nil map, which panics on write.
			holders = make(map[string]struct{})
			t.packages[nameVersion] = holders
		}
		holders[ip] = struct{}{}
	}

	t.peers[ip] = &peerRecord{
		ServingPort: req.ServingPort,
		Deadline:    time.Now().Add(t.timeout),
		Packages:    set,
	}

	log.Printf("tracker: announce ip=%s port=%d packages=%d",
		ip, req.ServingPort, len(set))
}

// Ping refreshes the deadline for ip. It reports false if the tracker has no
// entry for that IP, which the handler turns into the 404 that means
// "announce yourself". That 404 is a normal control signal, not an error: it
// is the whole tracker-restart self-healing path.
func (t *Tracker) Ping(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	rec, ok := t.peers[ip]
	if !ok {
		log.Printf("tracker: ping from unknown ip=%s", ip)
		return false
	}

	// An entry past its deadline that the sweeper has not reached yet is
	// already dead. Treating it as live here would let a daemon skip
	// re-announcing after an outage longer than the timeout, leaving the
	// index holding whatever stale list it last sent.
	if time.Now().After(rec.Deadline) {
		t.removeFromIndex(ip)
		delete(t.peers, ip)
		log.Printf("tracker: ping from expired ip=%s", ip)
		return false
	}

	rec.Deadline = time.Now().Add(t.timeout)
	log.Printf("tracker: ping ip=%s", ip)
	return true
}

// Peers returns up to MaxPeers live holders of nameVersion. Exact match only
// -- no prefix, no fuzzy matching.
//
// The result is never nil: a miss is an empty list, which the handler renders
// as {"peers": []}. A nil slice would marshal to `null` and break that.
func (t *Tracker) Peers(nameVersion string) []proto.PeerInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]proto.PeerInfo, 0, MaxPeers)

	holders, ok := t.packages[nameVersion]
	if !ok {
		log.Printf("tracker: query pkg=%q -> 0 peers", nameVersion)
		return out
	}

	now := time.Now()

	// Map iteration order is random, so which MaxPeers holders a caller
	// gets varies between requests. That is not a problem to fix: it
	// spreads load across holders for free.
	for ip := range holders {
		if len(out) == MaxPeers {
			break
		}

		rec, ok := t.peers[ip]
		if !ok {
			continue // swept between reads
		}
		if now.After(rec.Deadline) {
			continue // expired; the sweeper has not got to it yet
		}

		out = append(out, proto.PeerInfo{IP: ip, Port: rec.ServingPort})
	}

	log.Printf("tracker: query pkg=%q -> %d peers", nameVersion, len(out))
	return out
}

// Sweep deletes entries past their deadline and reports how many went. Expiry
// is silent: there is nobody to notify.
//
// This holds the write lock for the whole scan, which blocks every request.
// Acceptable while state is a map of at most a few thousand entries swept
// every 15s.
func (t *Tracker) Sweep() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	dropped := 0

	for ip, rec := range t.peers {
		if now.After(rec.Deadline) {
			// Index first: removeFromIndex reads t.peers[ip], so
			// deleting the record first would silently strip
			// nothing and leak the peer into the index forever.
			t.removeFromIndex(ip)
			delete(t.peers, ip)
			dropped++

			log.Printf("tracker: expired ip=%s (%v past deadline)",
				ip, now.Sub(rec.Deadline).Truncate(time.Second))
		}
	}
	return dropped
}

// RunSweeper blocks, sweeping on a ticker. Run it in a goroutine.
func (t *Tracker) RunSweeper() {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		t.Sweep()
	}
}

// removeFromIndex strips ip from every package it was listed under.
//
// CALLER MUST HOLD THE WRITE LOCK. It cannot take one itself: sync.RWMutex is
// not reentrant, so a locked caller calling a locking helper deadlocks against
// itself.
func (t *Tracker) removeFromIndex(ip string) {
	rec, ok := t.peers[ip]
	if !ok {
		return
	}

	// Iterate the peer's own list, not the global index, which may be
	// orders of magnitude larger.
	for nameVersion := range rec.Packages {
		holders, ok := t.packages[nameVersion]
		if !ok {
			continue
		}
		delete(holders, ip)

		// Don't leak empty holder sets; over a long uptime they are a
		// slow memory leak keyed by every package ever announced.
		if len(holders) == 0 {
			delete(t.packages, nameVersion)
		}
	}
}
