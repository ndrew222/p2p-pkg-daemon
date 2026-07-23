package discovery

import (
	"errors"
	"testing"
)

type fakeTracker struct { // stand in for real tracker
	pings     int
	announces int
	lastPort  int
	lastPkgs  []string

	pingErr     error
	announceErr error
}

func (f *fakeTracker) Ping() error {
	f.pings++
	return f.pingErr
}

func (f *fakeTracker) Announce(port int, pkgs []string) error {
	f.announces++
	f.lastPort = port
	f.lastPkgs = pkgs
	return f.announceErr
}

type fakeCache struct {
	pkgs []string
	err  error
}

func (f *fakeCache) Scan() ([]string, error) { return f.pkgs, f.err }

func TestTick(t *testing.T) {
	tests := []struct {
		name          string
		registered    bool
		pingErr       error
		wantPings     int
		wantAnnounces int
	}{
		{
			name:          "empty cache stays quiet",
			registered:    false,
			pingErr:       nil,
			wantPings:     0,
			wantAnnounces: 0,
		},
		{
			name:          "registered pings",
			registered:    true,
			pingErr:       nil,
			wantPings:     1,
			wantAnnounces: 0,
		},
		{
			name:          "forgotten reannounces",
			registered:    true,
			pingErr:       ErrUnknownPeer,
			wantPings:     1,
			wantAnnounces: 1,
		},
		{
			name:          "network error just retries",
			registered:    true,
			pingErr:       errors.New("connection refused"),
			wantPings:     1,
			wantAnnounces: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &fakeTracker{pingErr: tc.pingErr}
			ca := &fakeCache{pkgs: []string{"nginx-1.24.0"}}

			k := NewKeepAlive(tr, ca, 9310, nil)
			k.registered = tc.registered

			k.tick()

			if tr.pings != tc.wantPings {
				t.Errorf("pings = %d, want %d", tr.pings, tc.wantPings)
			}
			if tr.announces != tc.wantAnnounces {
				t.Errorf("announces = %d, want %d", tr.announces, tc.wantAnnounces)
			}
		})
	}
}

// TestAnnounce covers registration: what we send, and whether we end up
// believing we are on the tracker's list.
func TestAnnounce(t *testing.T) {
	tests := []struct {
		name            string
		startRegistered bool
		cachePkgs       []string
		cacheErr        error
		announceErr     error
		wantAnnounces   int
		wantRegistered  bool
	}{
		{
			name:            "non-empty list registers us",
			startRegistered: false,
			cachePkgs:       []string{"nginx-1.24.0"},
			wantAnnounces:   1,
			wantRegistered:  true,
		},
		{
			name:            "empty list deregisters us",
			startRegistered: true,
			cachePkgs:       nil,
			wantAnnounces:   1,
			wantRegistered:  false,
		},
		{
			name:            "scan failure changes nothing",
			startRegistered: true,
			cacheErr:        errors.New("permission denied"),
			wantAnnounces:   0,
			wantRegistered:  true,
		},
		{
			name:            "failed announce leaves us unregistered",
			startRegistered: false,
			cachePkgs:       []string{"nginx-1.24.0"},
			announceErr:     errors.New("connection refused"),
			wantAnnounces:   1,
			wantRegistered:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &fakeTracker{announceErr: tc.announceErr}
			ca := &fakeCache{pkgs: tc.cachePkgs, err: tc.cacheErr}

			k := NewKeepAlive(tr, ca, 9310, nil)
			k.registered = tc.startRegistered

			k.announce()

			if tr.announces != tc.wantAnnounces {
				t.Errorf("announces = %d, want %d", tr.announces, tc.wantAnnounces)
			}
			if k.registered != tc.wantRegistered {
				t.Errorf("registered = %v, want %v", k.registered, tc.wantRegistered)
			}
		})
	}
}
