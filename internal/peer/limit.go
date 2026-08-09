package peer

// ADR-002's admission control for the seeding side: two NON-BLOCKING
// semaphores, one global and one keyed by remote IP, and a 503 the moment
// either is full.
//
// This is admission control, not the bandwidth management AGENTS.md puts out
// of scope. It sets no rate, no throughput floor and no deadline, and an
// accepted transfer runs exactly as fast as it otherwise would. It answers one
// question at request time -- is there a free slot -- and if not says so
// immediately and truthfully.
//
// Nothing reclaims a held slot. The body transfer is unbounded in time by
// design (a slow peer is out of scope exactly as a slow mirror is), so a slot
// is held until the requester finishes or goes away, and any limit set here is
// a limit an adversary can pin at its ceiling. That is why the global cap
// alone was judged insufficient and both are present: the global one confines
// the damage to seeding so the facade's outbound fetches and the tracker
// keep-alive keep their share of a per-process descriptor budget, and the
// per-IP one stops a single source taking every seeding slot.
//
// A distributed attacker defeats the per-IP cap by construction and falls back
// on the global one. That is an honest limit of ADR-002, not an oversight.

import (
	"fmt"
	"sync"
)

// seedLimiter holds both caps and the current occupancy. A limit of 0 (or
// less) means unlimited, which is the default: ADR-002 justifies building the
// mechanism from the hostile-peer expectation but explicitly does not justify
// a number, because nobody has measured one.
type seedLimiter struct {
	maxGlobal int
	maxPerIP  int

	mu     sync.Mutex
	global int
	perIP  map[string]int
}

func newSeedLimiter(maxGlobal, maxPerIP int) *seedLimiter {
	return &seedLimiter{
		maxGlobal: maxGlobal,
		maxPerIP:  maxPerIP,
		perIP:     make(map[string]int),
	}
}

// refusal is what a rejected request needs in its log line. ADR-002 requires
// diagnostics that name which cap fired and for which IP, because an attack
// and a misconfigured ceiling look identical in a bare count and have opposite
// remedies.
type refusal struct {
	ip       string
	reason   string
	inFlight string
}

// acquire takes one slot from each cap, or none at all.
//
// It never blocks and never queues: the caller answers 503 immediately. On
// refusal it reports which cap fired; on success it returns the release
// function, which is idempotent so a handler may defer it freely.
func (l *seedLimiter) acquire(ip string) (release func(), refused refusal, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// The global cap is checked first because it is the one protecting the
	// resource actually at risk -- the process-wide descriptor budget the
	// seeder shares with the facade and the keep-alive. When both are full
	// the operator wants to hear about that one.
	if l.maxGlobal > 0 && l.global >= l.maxGlobal {
		return nil, refusal{
			ip:       ip,
			reason:   fmt.Sprintf("the global max_concurrent_seeds cap of %d is full", l.maxGlobal),
			inFlight: l.inFlightLocked(ip),
		}, false
	}
	if l.maxPerIP > 0 && l.perIP[ip] >= l.maxPerIP {
		return nil, refusal{
			ip:       ip,
			reason:   fmt.Sprintf("the max_concurrent_seeds_per_ip cap of %d is full for this IP", l.maxPerIP),
			inFlight: l.inFlightLocked(ip),
		}, false
	}

	l.global++
	l.perIP[ip]++

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.global--
			// Deleting at zero keeps the map from growing without
			// bound as source addresses churn -- otherwise a
			// hostile peer rotating addresses turns a cap meant to
			// protect memory into a way of consuming it.
			if l.perIP[ip]--; l.perIP[ip] <= 0 {
				delete(l.perIP, ip)
			}
		})
	}, refusal{}, true
}

// inFlightLocked renders the occupancy for a diagnostic. CALLER HOLDS l.mu.
func (l *seedLimiter) inFlightLocked(ip string) string {
	return fmt.Sprintf("%d total, %d from %s", l.global, l.perIP[ip], ip)
}
