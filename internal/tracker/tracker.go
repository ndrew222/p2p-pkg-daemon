package tracker

import (
	"log"  // print to stderr with timestamps
	"sync" // concurrency primitives, used for sync.RWMutex
	"time" // timestamps and duration, provide clocks

	"github.com/ndrew222/p2p-pkg-daemon/internal/proto" //import from proto, separate package (PeerID, AnnounceRequest, PingRequest and PeerInfo live there)
)

// LeaseTTL (Lease time to live) is how long a peer's registration survives without a heartbeat
// Daemons should ping  within window
// Peers heartbeat into the tracker instead of tracker trying to reach every peer's network through NATs and firewalls, but pretty much impossible
const LeaseTTL = 60 * time.Second

// background sweeper removes dead peers every 15s
// stale peers only last for 15s
const SweepInterval = 15 * time.Second

// peerRecord is what we remember about one live peer
// lower case: unexported, private to tracker, no one outside can construct or inspect one
type peerRecord struct {
	Addr     string
	LastSeen time.Time
	// Go has no set type to help find which CIDs a peer holds
	// Fake it using a map for its keys, and assign cheapest value slot: struct{}, 0 bytes per entry
	CIDs map[string]struct{} // set of CIDs this peer claims to hold
}

// Tracker is the in-memory registry. Safe for concurrent use
// all 3 fields lower case, kept private, ensures others from a handler with no lock can write it
type Tracker struct {
	mu    sync.RWMutex                         // above data to guard it
	peers map[proto.PeerID]*peerRecord         // who is alive, where, holding what. use a pointer to prevent modifying a copy. If modified a copy, lease never renew as map remains unchanged, every peer expires every 60s
	cids  map[string]map[proto.PeerID]struct{} // CID -> set of holders. two maps pointing at each other.
}

// New returns an empty Tracker
// all maps must be make-d before use otherwise writing to an empty (nil) map panics
// this constructor exists to ensure that all maps are initialised
// &tracker{} constructs a tracker, takes its address and returns the pointer, prevent copying a mutex (sync.RWMutex)
func New() *Tracker {
	return &Tracker{
		peers: make(map[proto.PeerID]*peerRecord),
		cids:  make(map[string]map[proto.PeerID]struct{}),
	}
}

// Announce registers or refreshes a peer and replaces its CID set.
func (t *Tracker) Announce(req *proto.AnnounceRequest) {
	// write lock, no one else (writer or reader) gets in until wew unlock
	// announce mutates both maps, so need exclusivity to prevent data corruption
	t.mu.Lock()
	// schedule unlock when function returns. ensures always unlock after announce is done
	// written right after lock() to prevent early returns in the future , and lock is held forever
	defer t.mu.Unlock()

	// Remove the peer's old CID claims before installing the new ones,
	// otherwise a peer that drops a package would still be listed for it (then list grows forever)
	t.removePeerFromCIDs(req.PeerID)

	set := make(map[string]struct{}, len(req.CIDs))

	// loops over announched CIDs and add each CID to set. Ignore index
	for _, cid := range req.CIDs {
		set[cid] = struct{}{}

		// ok (true) if key exists, false it not
		// distinguish between absent key and present key with zero value
		holders, ok := t.cids[cid]

		// if key is absent, map is nil. writing to it panics. So make a fresh set and store it
		if !ok {
			holders = make(map[proto.PeerID]struct{})
			t.cids[cid] = holders
		}
		holders[req.PeerID] = struct{}{}
	}

	// install new record
	// overwrites any existing record
	// & gives a pointer
	t.peers[req.PeerID] = &peerRecord{
		Addr:     req.Addr,
		LastSeen: time.Now(), // starts lease clock
		CIDs:     set,
	}

	log.Printf("tracker: announce peer=%q addr=%q cids=%d",
		req.PeerID, req.Addr, len(req.CIDs))
}

// Ping renews a peer's lease. Returns false if the peer is unknown
// the daemon must Announce before it can Ping
func (t *Tracker) Ping(req *proto.PingRequest) bool {
	// mutates LastSeen, so need mutex
	t.mu.Lock()
	defer t.mu.Unlock()

	// prevents un-announced daemons (peer we have never seen) from pinging
	// prevents attacks that can flood the pings if anyone can ping it
	// annouce is registration, ping is for renewal
	rec, ok := t.peers[req.PeerID]
	if !ok {
		log.Printf("tracker: ping from unknown peer=%q", req.PeerID)
		return false
	}

	// rec is *peerRecord, so u are writing through the pinter into actual record in map instead of a copy
	rec.LastSeen = time.Now()
	rec.Addr = req.Addr // address may have changed
	log.Printf("tracker: ping peer=%q", req.PeerID)
	return true
}

// Peers returns the live peers holding cid
func (t *Tracker) Peers(cid string) []proto.PeerInfo {
	// readlock, function only reads
	// choosing RWMutex allows concurrent read locks in parallel for many goroutines
	t.mu.RLock()
	defer t.mu.RUnlock()

	holders, ok := t.cids[cid]
	if !ok {
		return nil
	}

	now := time.Now()
	out := make([]proto.PeerInfo, 0, len(holders))
	for id := range holders {
		rec, ok := t.peers[id]
		if !ok {
			continue // swept between reads; skip
		}

		// now.Sub() returns duration. longer than TTL and the peer is dead
		if now.Sub(rec.LastSeen) > LeaseTTL {
			continue // stale; sweeper hasn't got to it yet
		}

		// builds the response. only respond with ID and address
		out = append(out, proto.PeerInfo{PeerID: id, Addr: rec.Addr})
	}

	log.Printf("tracker: query cid=%q -> %d peers", cid, len(out))
	return out
}

// Sweep removes peers whose lease has expired. Returns how many it dropped.
// all request blocked while sweeping, its ok since sweeps are fast and infrequent.
func (t *Tracker) Sweep() int {
	// write lock needed as sweep deletes
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	dropped := 0

	// range over t.peers gives both key (id) and value (rec) as you need ID to delete and record to check the lease
	// removes CID first them record. Otherwise removePeerFromCIDs looks up a peer thats already gone, finds nothing, silent corruption
	for id, rec := range t.peers {
		if now.Sub(rec.LastSeen) > LeaseTTL {
			t.removePeerFromCIDs(id) // cleanup
			delete(t.peers, id)      // then, deletion
			dropped++

			// kills daemon, wait, and prints so eviction is visible
			// truncate to round the timing for readability, otherwise its long decimal
			log.Printf("tracker: expired peer=%q (last seen %v ago)",
				id, now.Sub(rec.LastSeen).Truncate(time.Second))
		}
	}
	return dropped // to know how many were dropped (int)
}

// RunSweeper blocks, sweeping on a ticker. Run it in a goroutine.
// fires repeatedly on an interval
// every 15s, ticker sends current time down that channel .C
// no returns, so blocks forever by design, so caller must run it in its own goroutine (go t.RunSweeper())
// go before a function call starts new goroutine, sweeper runs alongside main at once
func (t *Tracker) RunSweeper() {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()  // release tickers resource when funcion exits
	for range ticker.C { // each recieve blocks until next tick arrives so loop body runs exactly once per interval, and between ticks the goroutine is parked (deschedule, no CPU consumption)
		t.Sweep()

	}
}

// removePeerFromCIDs strips a peer from every CID it was listed under
// CALLER MUST HOLD THE WRITE LOCK, as it does not lock on its own
// cant lock itself as sync.RWMutex is not reentrant. If announce holds the lock and then calls a function that tries to lock agin, goroutine waits for a lock it itself is holding = deadlock. goroutine stops forever
// lower case: private helper
func (t *Tracker) removePeerFromCIDs(id proto.PeerID) {

	// peer isnt there, nothing to strip so return
	rec, ok := t.peers[id]
	if !ok {
		return
	}

	// iterate over peer record CID instead of global (t.CIDs) which can be huge
	for cid := range rec.CIDs {

		// holders: map, a reference type, modifying this modify real set inside t.cids
		// get holder set for that CID, remove holder
		holders, ok := t.cids[cid]
		if !ok {
			continue
		}
		delete(holders, id)

		// remove CID entry entirely is holder deleted is the last holder
		// prevent accumulation of empty sets in t.cids. Can lead to memory leak
		if len(holders) == 0 {
			delete(t.cids, cid) // don't leak empty CID entries
		}
	}
}
