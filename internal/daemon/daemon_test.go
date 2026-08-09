package daemon

import (
	"encoding/json"
	"io"
	"net"
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

// ADR-008 end to end, and specifically the case the ruling was about: a cached
// file that SanityFilter dropped because the catalogue had never heard of it.
// No cache event will ever revisit that file -- nothing about it changes -- so
// without a reload that re-announces, the daemon under-announces until restart.
func TestCatalogueRewriteReachesTheTracker(t *testing.T) {
	previousSettle := repoSettleDelay
	repoSettleDelay = testSettle
	t.Cleanup(func() { repoSettleDelay = previousSettle })

	cacheDir := t.TempDir()
	repoDBDir := t.TempDir()

	// The catalogue knows nginx; the cache holds curl. writePackage writes
	// 13 bytes, which is the size the rewritten catalogue will claim.
	writeRepoDB(t, repoDBDir, "FreeBSD-ports", []fixtureRow{
		{"nginx", "1.24.0_2", 13, hash64('a')},
	})
	writePackage(t, cacheDir, "curl-8.6.0.pkg")

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
			TrackerURL:  srv.URL,
			FacadeAddr:  "127.0.0.1:9001",
			ServingAddr: "0.0.0.0:9002",
			CacheDir:    cacheDir,
			RepoDBDir:   repoDBDir,
		},
	}

	d.mu.Lock()
	err := d.openRepositoriesLocked()
	if err == nil {
		err = d.startDiscoveryLocked()
	}
	d.mu.Unlock()
	if err != nil {
		t.Fatalf("startup: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })

	first := waitForAnnounce(t, announces)
	if len(first.Packages) != 0 {
		t.Fatalf("startup announce = %v, want an empty list: curl is not in the catalogue yet", first.Packages)
	}

	// `pkg update`: the catalogue is replaced, and now lists curl.
	if err := os.RemoveAll(filepath.Join(repoDBDir, "FreeBSD-ports")); err != nil {
		t.Fatal(err)
	}
	writeRepoDB(t, repoDBDir, "FreeBSD-ports", []fixtureRow{
		{"nginx", "1.24.0_2", 13, hash64('a')},
		{"curl", "8.6.0", 13, hash64('d')},
	})

	deadline := time.After(10 * time.Second)
	for {
		select {
		case req := <-announces:
			if slices.Contains(req.Packages, "curl-8.6.0") {
				return // reload -> rescan -> re-announce
			}
		case <-deadline:
			t.Fatal("the rewritten catalogue never reached the tracker")
		}
	}
}

// The repository watcher's lifetime is discovery's, because SIGHUP restarts
// discovery on a superset of the conditions that stale it.
func TestRepoWatcherLifetimeIsDiscoverys(t *testing.T) {
	repoDBDir := t.TempDir()
	writeRepoDB(t, repoDBDir, "FreeBSD-ports", []fixtureRow{{"nginx", "1.24.0_2", 13, hash64('a')}})

	d := &Daemon{
		config: &config.DaemonConfig{
			TrackerURL:  "http://127.0.0.1:1",
			FacadeAddr:  "127.0.0.1:9001",
			ServingAddr: "0.0.0.0:9002",
			CacheDir:    t.TempDir(),
			RepoDBDir:   repoDBDir,
		},
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.openRepositoriesLocked(); err != nil {
		t.Fatal(err)
	}
	if err := d.startDiscoveryLocked(); err != nil {
		t.Fatalf("startDiscoveryLocked() = %v", err)
	}
	if d.repoWatcher == nil {
		t.Fatal("discovery started without a repository watcher")
	}
	d.stopDiscoveryLocked()
	if d.repoWatcher != nil {
		t.Error("stopDiscoveryLocked left the repository watcher behind")
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

// freePort returns a port nothing is listening on. The facade needs a real
// address because startHTTPServerLocked calls ListenAndServe, and a fixed port
// would make the test collide with whatever else is on the machine.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// The facade is mounted, not merely constructed. Until the repository database
// reader landed the daemon served an empty mux on facade_addr, so pkg got Go's
// stock "404 page not found" for every request and no package could ever be
// fetched through it.
//
// The probe is a metadata path answered by a stand-in upstream, which under
// ADR-005 is what `pkg update` does through the real daemon. It used to be the
// same path expecting the facade's 404 refusal -- a probe that would now pass
// against a facade wired to nothing at all, since the refusal needed no
// upstream. Relayed bytes cannot be produced by an empty mux or by a facade
// whose upstream is unset, so this proves the wiring end to end: config →
// Facade.UpstreamURL → the mirror.
func TestFacadeIsMountedOnFacadeAddr(t *testing.T) {
	const catalogue = "signed catalogue bytes"
	cacheDir := t.TempDir()
	repoDir := t.TempDir()
	writeRepoDB(t, repoDir, "FreeBSD-ports", []fixtureRow{
		{"nginx", "1.24.0_2", 1234, hash64('a')},
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stable/FreeBSD:15:amd64/latest/meta.conf" {
			t.Errorf("upstream got %q, want the path pkg asked for", r.URL.Path)
		}
		_, _ = io.WriteString(w, catalogue)
	}))
	t.Cleanup(upstream.Close)

	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ack"}`))
	}))
	t.Cleanup(tracker.Close)

	facadeAddr := freePort(t)
	d := &Daemon{
		config: &config.DaemonConfig{
			TrackerURL:  tracker.URL,
			FacadeAddr:  facadeAddr,
			ServingAddr: "0.0.0.0:9002",
			CacheDir:    cacheDir,
			RepoDBDir:   repoDir,
			TempDir:     t.TempDir(),
			// ADR-006: required, no default, ${ABI} already expanded by
			// the time the daemon starts.
			UpstreamURL: upstream.URL,
		},
		running: true,
	}
	t.Cleanup(func() { _ = d.Stop() })

	d.mu.Lock()
	if err := d.openRepositoriesLocked(); err != nil {
		d.mu.Unlock()
		t.Fatalf("openRepositoriesLocked() = %v", err)
	}
	if err := d.startDiscoveryLocked(); err != nil {
		d.mu.Unlock()
		t.Fatalf("startDiscoveryLocked() = %v", err)
	}
	if err := d.startHTTPServerLocked(); err != nil {
		d.mu.Unlock()
		t.Fatalf("startHTTPServerLocked() = %v", err)
	}
	d.mu.Unlock()

	url := "http://" + facadeAddr + "/stable/FreeBSD:15:amd64/latest/meta.conf"
	var resp *http.Response
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 relayed from upstream", resp.StatusCode)
	}
	if string(body) != catalogue {
		t.Errorf("body = %q; want the relayed catalogue, not the empty mux's 404", body)
	}
}

// Facade.Check is what refuses to serve without a catalogue, and the daemon
// must surface that rather than listening anyway.
func TestStartHTTPServerRefusesWithoutARepositoryDatabase(t *testing.T) {
	d := &Daemon{
		config: &config.DaemonConfig{
			FacadeAddr: freePort(t),
			TempDir:    t.TempDir(),
		},
	}
	d.mu.Lock()
	err := d.startHTTPServerLocked()
	d.mu.Unlock()
	if err == nil {
		t.Fatal("startHTTPServerLocked() without a repository database = nil, want an error")
	}
}
