package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/internal/config"
	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
)

func TestCacheSourceScan(t *testing.T) {
	cacheDir := t.TempDir()
	writePackage(t, cacheDir, "nginx-1.24.0_2.pkg")
	writePackage(t, cacheDir, "curl-8.6.0.pkg")
	// Dropped by SanityFilter: no digit-initial version, so it has no
	// name-version to announce.
	writePackage(t, cacheDir, "README.txt")

	src := cacheSource{watcher: New(cacheDir, nil, nil, nil)}
	got, err := src.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	slices.Sort(got)
	want := []string{"curl-8.6.0", "nginx-1.24.0_2"}
	if !slices.Equal(got, want) {
		t.Errorf("Scan() = %v, want %v", got, want)
	}

	// Whatever comes out must survive the wire layer, or the announce is
	// rejected before it is sent.
	req := proto.AnnounceRequest{ServingPort: 4711, Packages: got}
	if err := req.Validate(); err != nil {
		t.Errorf("scanned list is not announceable: %v", err)
	}
}

// The pkg cache is read-only to this daemon. A missing cache directory is an
// error, never something to create.
func TestWatcherStartDoesNotCreateCacheDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-cache")

	w := New(missing, nil, nil, nil)
	if err := w.Start(); err == nil {
		w.Stop()
		t.Fatal("Start() on a missing cache dir = nil, want an error")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("Start() created %q; the pkg cache is read-only", missing)
	}
}

func TestWatcherStartRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	w := New(file, nil, nil, nil)
	if err := w.Start(); err == nil {
		w.Stop()
		t.Error("Start() on a regular file = nil, want an error")
	}
}

// The payoff of the whole wiring: a package appearing in the pkg cache must
// reach the tracker without anyone restarting the daemon. This drives the real
// startDiscoveryLocked path against a stand-in tracker.
func TestCacheChangeReachesTheTracker(t *testing.T) {
	cacheDir := t.TempDir()

	announces := make(chan proto.AnnounceRequest, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	t.Cleanup(srv.Close)

	d := &Daemon{
		config: &config.DaemonConfig{
			TrackerURL: srv.URL,
			// The announced port comes from serving_addr. facade_addr is
			// deliberately a different port here, so a regression that
			// reintroduced the old single-address derivation would show
			// up as the wrong number on the wire.
			FacadeAddr:  "127.0.0.1:9001",
			ServingAddr: "0.0.0.0:9002",
			CacheDir:    cacheDir,
		},
	}

	d.mu.Lock()
	err := d.startDiscoveryLocked()
	d.mu.Unlock()
	if err != nil {
		t.Fatalf("startDiscoveryLocked: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })

	// Startup registration: an empty cache announces an empty list, which
	// is the deregistration path and is exactly right for a daemon with
	// nothing to serve.
	first := waitForAnnounce(t, announces)
	if len(first.Packages) != 0 {
		t.Errorf("startup announce = %v, want an empty list", first.Packages)
	}
	if first.ServingPort != 9002 {
		t.Errorf("servingPort = %d, want 9002 (the port half of serving_addr)", first.ServingPort)
	}

	// pkg installs something.
	writePackage(t, cacheDir, "nginx-1.24.0_2.pkg")

	// The watcher notices, nudges the keep-alive, and the full list goes up
	// unprompted -- no ping, no restart, no waiting for the 20s timer.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case req := <-announces:
			if slices.Contains(req.Packages, "nginx-1.24.0_2") {
				return // the wiring works
			}
		case <-deadline:
			t.Fatal("cache change never reached the tracker")
		}
	}
}

func waitForAnnounce(t *testing.T, ch <-chan proto.AnnounceRequest) proto.AnnounceRequest {
	t.Helper()
	select {
	case req := <-ch:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("no announce arrived")
		return proto.AnnounceRequest{}
	}
}

func writePackage(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("package bytes"), 0644); err != nil {
		t.Fatal(err)
	}
}
