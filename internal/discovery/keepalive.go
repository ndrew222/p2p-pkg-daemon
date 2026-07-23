package discovery

import (
	"errors"
	"log"
	"time"
)

// discovery.Client will satisfy this once wire spec v0.2 lands.
type Tracker interface {
	Ping() error // ErrUnknownPeer if forgotten
	Announce(port int, packages []string) error
}

// Cache is the package source, cache watcher satisfies this
type Cache interface {
	Scan() ([]string, error)
}

type KeepAlive struct {
	tracker    Tracker
	cache      Cache
	port       int // where daemon listens for other peers wanting to download
	interval   time.Duration
	changed    <-chan struct{} // watcher nudges here when the cache changes
	registered bool            // whether the tracker knows about us, so we can ping it
}

func NewKeepAlive(t Tracker, c Cache, port int, changed <-chan struct{}) *KeepAlive {
	return &KeepAlive{
		tracker:  t,
		cache:    c,
		port:     port,
		interval: PingInterval,
		changed:  changed,
	}
}

// announce scans the cache and pushes the full list
func (k *KeepAlive) announce() {
	pkgs, err := k.cache.Scan()
	if err != nil {
		log.Printf("keepalive: scan failed: %v", err)
		return // do not treat as empty, will deregister
	}
	if err := k.tracker.Announce(k.port, pkgs); err != nil {
		log.Printf("keepalive: announce failed: %v", err)
		return // tracker stored nothing, stay unregistered
	}
	k.registered = len(pkgs) > 0 // if no pkgs, false, else true
}

// tick is one beat of the heartbeat.
func (k *KeepAlive) tick() {
	if !k.registered {
		return // empty cache, stay quiet
	}
	err := k.tracker.Ping()
	switch {
	case err == nil:
		// re-announced already, nothing to do
	case errors.Is(err, ErrUnknownPeer):
		log.Printf("keepalive: tracker forgot us; re-announcing")
		k.announce()
	default:
		log.Printf("keepalive: ping failed: %v", err)
	}
}

// Run blocks until done is closed. Start it in a goroutine.
func (k *KeepAlive) Run(done <-chan struct{}) {
	k.announce() // startup registration

	t := time.NewTicker(k.interval) // 20s, defined in client.go
	defer t.Stop()                  // to stop the timer when surrounding function returns

	for {
		select {
		case <-done: // shutdown signal, exit gracefully
			return
		case <-k.changed: // cache changed, re-announce
			k.announce()
		case <-t.C: // timer fired, ping the tracker
			k.tick()
		}
	}
}
