package daemon

// The seed server's wiring (UC-06, HANDOFF §5.4). The seeder's own behaviour
// -- response codes, the two caps, the path rule -- is tested in
// internal/peer; what is under test here is that it is mounted at all, on the
// right address, and connected to the keep-alive.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/internal/config"
	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
)

// The defect this closes: the daemon announced serving_addr and nothing
// listened on it, so every peer acting on our tracker entry dialled and got
// connection-refused. That correctly did not blacklist us -- a dial failure
// never does -- so the cost was one wasted attempt per peer, paid by the rest
// of the swarm, and it was invisible in our own logs.
//
// A cached package is the probe, because it is the one request that proves the
// listener, the peer path rule and the cache-backed source all agree.
func TestSeedServerIsMountedOnServingAddr(t *testing.T) {
	cacheDir := t.TempDir()
	writePackage(t, cacheDir, "nginx-1.24.0_2.pkg")

	servingAddr := freePort(t)
	d := &Daemon{
		config: &config.DaemonConfig{
			// A different port from the facade's, so a regression
			// that mounted the seeder on the wrong listener shows
			// up as the wrong number rather than as a pass.
			FacadeAddr:  "127.0.0.1:9001",
			ServingAddr: servingAddr,
			CacheDir:    cacheDir,
		},
	}
	d.mu.Lock()
	err := d.startSeedServerLocked()
	d.mu.Unlock()
	if err != nil {
		t.Fatalf("startSeedServerLocked: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })

	body, code := getWithRetry(t, "http://"+servingAddr+"/pkg/nginx-1.24.0_2")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if string(body) != "package bytes" {
		t.Errorf("body = %q, want the cached file's contents", body)
	}

	// The peer namespace is deliberately NOT the facade's. A seeding daemon
	// must not be a syntactically valid pkg mirror, so the facade's own
	// path shape must not work here.
	if _, code := getWithRetry(t, "http://"+servingAddr+"/latest/All/nginx-1.24.0_2.pkg"); code == http.StatusOK {
		t.Error("the seed server answered a facade-shaped path; the two namespaces must not converge")
	}
}

// UC-06 §5b: asked for something we do not hold, the seeder answers 404 and
// sends a FULL re-announce -- if one entry has drifted, others may have too.
// This drives the real wiring, so it also pins that the seeder's nudge reaches
// the keep-alive rather than a channel nobody reads.
func TestSeedServer404TriggersAFullReAnnounce(t *testing.T) {
	cacheDir := t.TempDir()

	announces := make(chan proto.AnnounceRequest, 32)
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/announce" {
			body, _ := io.ReadAll(r.Body)
			var req proto.AnnounceRequest
			if err := json.Unmarshal(body, &req); err == nil {
				select {
				case announces <- req:
				default:
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ack"}`))
	}))
	t.Cleanup(tracker.Close)

	servingAddr := freePort(t)
	d := &Daemon{
		config: &config.DaemonConfig{
			TrackerURL:  tracker.URL,
			FacadeAddr:  "127.0.0.1:9001",
			ServingAddr: servingAddr,
			CacheDir:    cacheDir,
		},
	}
	d.mu.Lock()
	if err := d.startDiscoveryLocked(); err != nil {
		d.mu.Unlock()
		t.Fatalf("startDiscoveryLocked: %v", err)
	}
	if err := d.startSeedServerLocked(); err != nil {
		d.mu.Unlock()
		t.Fatalf("startSeedServerLocked: %v", err)
	}
	d.mu.Unlock()
	t.Cleanup(func() { _ = d.Stop() })

	waitForAnnounce(t, announces) // the startup registration
	writePackage(t, cacheDir, "curl-8.6.0.pkg")

	_, code := getWithRetry(t, "http://"+servingAddr+"/pkg/gone-1.0")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}

	// The announce that follows is the WHOLE list, never a correction for
	// the one package that was missing.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case req := <-announces:
			if slices.Contains(req.Packages, "curl-8.6.0") {
				return
			}
		case <-deadline:
			t.Fatal("a 404 on the seed server never produced a full re-announce")
		}
	}
}

// A seeder with no cache_dir would answer 404 to every request and re-announce
// on each one. Report it rather than serve it.
func TestSeedServerRefusesWithoutACacheDir(t *testing.T) {
	d := &Daemon{config: &config.DaemonConfig{ServingAddr: freePort(t)}}
	d.mu.Lock()
	err := d.startSeedServerLocked()
	d.mu.Unlock()
	if err == nil {
		_ = d.Stop()
		t.Fatal("startSeedServerLocked() with no cache_dir = nil, want an error")
	}
}

// SIGHUP must move the seeder, not leave it on the old port while the tracker
// is told about the new one. The address the swarm is given and the address we
// listen on are the same setting, and they must not be able to drift apart.
func TestRestartMovesTheSeedServer(t *testing.T) {
	cacheDir := t.TempDir()
	writePackage(t, cacheDir, "nginx-1.24.0_2.pkg")

	first, second := freePort(t), freePort(t)
	d := &Daemon{
		config: &config.DaemonConfig{
			FacadeAddr:  "127.0.0.1:9001",
			ServingAddr: first,
			CacheDir:    cacheDir,
		},
	}
	d.mu.Lock()
	if err := d.startSeedServerLocked(); err != nil {
		d.mu.Unlock()
		t.Fatalf("startSeedServerLocked: %v", err)
	}
	d.config.ServingAddr = second
	err := d.startSeedServerLocked()
	d.mu.Unlock()
	if err != nil {
		t.Fatalf("restart on the new address: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })

	if _, code := getWithRetry(t, "http://"+second+"/pkg/nginx-1.24.0_2"); code != http.StatusOK {
		t.Errorf("new serving_addr status = %d, want 200", code)
	}
	if resp, err := http.Get("http://" + first + "/pkg/nginx-1.24.0_2"); err == nil {
		resp.Body.Close()
		t.Errorf("the old serving_addr is still listening (status %d); the previous listener must be closed", resp.StatusCode)
	}
}

// Stop must release serving_addr, or restarting the daemon fails to bind.
func TestStopClosesTheSeedServer(t *testing.T) {
	cacheDir := t.TempDir()
	addr := freePort(t)
	d := &Daemon{config: &config.DaemonConfig{ServingAddr: addr, CacheDir: cacheDir}}
	d.mu.Lock()
	if err := d.startSeedServerLocked(); err != nil {
		d.mu.Unlock()
		t.Fatalf("startSeedServerLocked: %v", err)
	}
	d.mu.Unlock()
	getWithRetry(t, "http://"+addr+"/pkg/anything-1.0") // ensure it is really up

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("serving_addr was not released by Stop: %v", err)
	}
	ln.Close()
}

// getWithRetry polls until the listener is accepting, because the server is
// started in a goroutine and the test would otherwise race it.
func getWithRetry(t *testing.T, url string) ([]byte, int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return body, resp.StatusCode
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
