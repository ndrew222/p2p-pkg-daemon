package tracker

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
)

func announce(t *testing.T, tr *Tracker, ip string, port int, packages ...string) {
	t.Helper()
	req := &proto.AnnounceRequest{ServingPort: port, Packages: packages}
	// The handler validates before calling Announce; assert the fixtures
	// would survive that, so a test cannot pass on a body the wire rejects.
	if err := req.Validate(); err != nil {
		t.Fatalf("test fixture is not a valid announce: %v", err)
	}
	tr.Announce(ip, req)
}

func ips(peers []proto.PeerInfo) map[string]int {
	out := make(map[string]int, len(peers))
	for _, p := range peers {
		out[p.IP] = p.Port
	}
	return out
}

// The complete life cycle from v0.2 §One complete life cycle, in order.
func TestLifeCycle(t *testing.T) {
	tr := New()
	const ip = "203.0.113.7"

	// daemon boots -> ping -> unknown IP -> 404
	if tr.Ping(ip) {
		t.Fatal("ping from an unregistered ip = true, want false (the 404)")
	}

	// -> announce -> registered
	announce(t, tr, ip, 4711, "nginx-1.24.0_2", "curl-8.6.0")
	if got := tr.Peers("nginx-1.24.0_2"); len(got) != 1 || got[0].Port != 4711 {
		t.Fatalf("after announce, peers = %v, want one entry on port 4711", got)
	}

	// every 20s -> ping -> refreshed
	if !tr.Ping(ip) {
		t.Fatal("ping from a registered ip = false, want true")
	}

	// daemon gets a new package -> unprompted announce with the full list
	announce(t, tr, ip, 4711, "nginx-1.24.0_2", "curl-8.6.0", "jq-1.7")
	if got := tr.Peers("jq-1.7"); len(got) != 1 {
		t.Fatalf("after re-announce, jq-1.7 peers = %v, want one entry", got)
	}

	// someone runs pkg clean -> empty announce -> entry deleted
	announce(t, tr, ip, 4711)
	if got := tr.Peers("nginx-1.24.0_2"); len(got) != 0 {
		t.Errorf("after empty announce, peers = %v, want none", got)
	}
	if tr.Ping(ip) {
		t.Error("ping after deregistration = true, want false")
	}
}

// The list is a full replacement, never a delta: a package dropped from the
// list must stop being served immediately.
func TestAnnounceReplacesRatherThanMerges(t *testing.T) {
	tr := New()
	const ip = "203.0.113.7"

	announce(t, tr, ip, 4711, "nginx-1.24.0_2", "curl-8.6.0")
	announce(t, tr, ip, 4711, "curl-8.6.0")

	if got := tr.Peers("nginx-1.24.0_2"); len(got) != 0 {
		t.Errorf("dropped package still listed: %v", got)
	}
	if got := tr.Peers("curl-8.6.0"); len(got) != 1 {
		t.Errorf("retained package = %v, want one entry", got)
	}
}

// A re-announce on a different port must move the peer, not leave the old one
// reachable.
func TestAnnounceUpdatesServingPort(t *testing.T) {
	tr := New()
	const ip = "203.0.113.7"

	announce(t, tr, ip, 4711, "nginx-1.24.0_2")
	announce(t, tr, ip, 5522, "nginx-1.24.0_2")

	got := tr.Peers("nginx-1.24.0_2")
	if len(got) != 1 || got[0].Port != 5522 {
		t.Fatalf("peers = %v, want a single entry on port 5522", got)
	}
}

// An empty announce from an IP that was never registered is a plain ack, not
// a crash and not a stored entry. It is how a fresh daemon probes the tracker.
func TestEmptyAnnounceFromUnknownIP(t *testing.T) {
	tr := New()
	announce(t, tr, "203.0.113.7", 4711)

	if tr.Ping("203.0.113.7") {
		t.Error("empty announce registered the peer, want no entry")
	}
	if n := len(tr.peers); n != 0 {
		t.Errorf("peers map holds %d entries, want 0", n)
	}
}

// A miss must be an empty list, not nil: nil marshals to `null` and the spec
// requires {"peers": []}.
func TestPeersMissIsEmptyNotNil(t *testing.T) {
	tr := New()
	got := tr.Peers("nothing-1.0")
	if got == nil {
		t.Fatal("Peers() on a miss = nil, want an empty slice")
	}
	if len(got) != 0 {
		t.Errorf("Peers() on a miss = %v, want empty", got)
	}
}

// Exact match only -- no prefix, no fuzzy matching.
func TestPeersExactMatchOnly(t *testing.T) {
	tr := New()
	announce(t, tr, "203.0.113.7", 4711, "nginx-1.24.0_2")

	for _, query := range []string{"nginx", "nginx-1.24.0", "nginx-1.24.0_2 ", "NGINX-1.24.0_2", ""} {
		if got := tr.Peers(query); len(got) != 0 {
			t.Errorf("Peers(%q) = %v, want no match", query, got)
		}
	}
}

func TestPeersCapsAtMaxPeers(t *testing.T) {
	tr := New()
	const pkg = "nginx-1.24.0_2"

	for i := range 10 {
		announce(t, tr, fmt.Sprintf("203.0.113.%d", i+1), 4711+i, pkg)
	}

	got := tr.Peers(pkg)
	if len(got) != MaxPeers {
		t.Fatalf("Peers() returned %d entries, want the cap of %d", len(got), MaxPeers)
	}
	// Whichever three come back must be distinct and real.
	seen := ips(got)
	if len(seen) != MaxPeers {
		t.Errorf("Peers() returned duplicate ips: %v", got)
	}
	for _, p := range got {
		if err := p.Validate(); err != nil {
			t.Errorf("peer %+v is not dialable: %v", p, err)
		}
	}
}

func TestExpiry(t *testing.T) {
	tr := NewWithTimeout(20 * time.Millisecond)
	const ip = "203.0.113.7"

	announce(t, tr, ip, 4711, "nginx-1.24.0_2")
	if len(tr.Peers("nginx-1.24.0_2")) != 1 {
		t.Fatal("peer not registered")
	}

	time.Sleep(40 * time.Millisecond)

	// Peers() must ignore the entry before the sweeper has run: a caller
	// must never be handed an address whose lease has lapsed.
	if got := tr.Peers("nginx-1.24.0_2"); len(got) != 0 {
		t.Errorf("expired peer still returned: %v", got)
	}
	// And a ping from it is not a refresh -- it has to re-announce, or the
	// index keeps whatever stale list it last sent.
	if tr.Ping(ip) {
		t.Error("ping from an expired peer = true, want false")
	}
}

func TestSweepFlushesExpiredEntries(t *testing.T) {
	tr := NewWithTimeout(20 * time.Millisecond)

	announce(t, tr, "203.0.113.7", 4711, "nginx-1.24.0_2")
	announce(t, tr, "198.51.100.4", 5522, "curl-8.6.0")

	if n := tr.Sweep(); n != 0 {
		t.Fatalf("Sweep() before any deadline dropped %d, want 0", n)
	}

	time.Sleep(40 * time.Millisecond)

	if n := tr.Sweep(); n != 2 {
		t.Errorf("Sweep() dropped %d, want 2", n)
	}
	// Both maps must come back empty. A leaked holder set is a slow memory
	// leak keyed by every package ever announced.
	if len(tr.peers) != 0 {
		t.Errorf("peers map holds %d entries after sweep, want 0", len(tr.peers))
	}
	if len(tr.packages) != 0 {
		t.Errorf("packages index holds %d entries after sweep, want 0", len(tr.packages))
	}
}

// Announcing then deregistering must leave no trace in the reverse index.
func TestIndexDoesNotLeak(t *testing.T) {
	tr := New()
	const ip = "203.0.113.7"

	announce(t, tr, ip, 4711, "nginx-1.24.0_2", "curl-8.6.0", "jq-1.7")
	announce(t, tr, ip, 4711)

	if len(tr.packages) != 0 {
		t.Errorf("packages index holds %v after deregistration, want empty", tr.packages)
	}
}

// A second daemon behind the same public IP overwrites the first. This is the
// known limitation the spec accepts, pinned so it changes deliberately.
func TestOneDaemonPerIP(t *testing.T) {
	tr := New()
	const ip = "203.0.113.7"

	announce(t, tr, ip, 4711, "nginx-1.24.0_2")
	announce(t, tr, ip, 5522, "curl-8.6.0")

	if got := tr.Peers("nginx-1.24.0_2"); len(got) != 0 {
		t.Errorf("first daemon survived a same-IP announce: %v", got)
	}
	if got := tr.Peers("curl-8.6.0"); len(got) != 1 || got[0].Port != 5522 {
		t.Errorf("second daemon = %v, want one entry on 5522", got)
	}
}

// The tracker is documented as safe for concurrent use; run it under -race.
func TestConcurrentAccess(t *testing.T) {
	tr := New()
	var wg sync.WaitGroup

	// Not the announce helper: it calls t.Fatalf, which is illegal from a
	// goroutine other than the one running the test.
	req := &proto.AnnounceRequest{
		ServingPort: 4711,
		Packages:    []string{"nginx-1.24.0_2", "curl-8.6.0"},
	}

	for i := range 8 {
		wg.Add(3)
		ip := fmt.Sprintf("203.0.113.%d", i+1)

		go func() {
			defer wg.Done()
			for range 50 {
				tr.Announce(ip, req)
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				tr.Ping(ip)
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				tr.Peers("nginx-1.24.0_2")
			}
		}()
	}

	wg.Wait()
}
