package daemon

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/internal/config"
)

const testCID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// freeAddr reserves a free loopback address then releases it, so ListenAndServe
// can bind it. There is an inherent tiny race; acceptable in tests.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free addr: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// fakeTracker stands in for the real tracker: it only needs to acknowledge
// announce/ping and count how many of each it saw.
type fakeTracker struct {
	srv       *httptest.Server
	announces *atomic.Int32
	pings     *atomic.Int32
}

func newFakeTracker(t *testing.T) *fakeTracker {
	t.Helper()
	f := &fakeTracker{announces: &atomic.Int32{}, pings: &atomic.Int32{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/announce":
			f.announces.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/ping":
			f.pings.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// writeConfig persists a config to a temp file and returns its path.
func writeConfig(t *testing.T, cfg *config.DaemonConfig) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return path
}

// newDaemon builds a Daemon with a runtime config (what flags+file merged into)
// and the path to the config file.
func newDaemon(cfg *config.DaemonConfig, configPath string) *Daemon {
	return &Daemon{
		config:     cfg,
		configPath: configPath,
		peerID:     "alice",
		cids:       []string{testCID},
		running:    true,
	}
}

func get(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// waitFor polls fn until it returns true or the deadline passes.
func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// TestReloadListenAddrChange (H1): a valid reload with a changed listen_addr
// restarts the HTTP server on the new address and closes the old one.
func TestReloadListenAddrChange(t *testing.T) {
	f := newFakeTracker(t)
	addr1 := freeAddr(t)
	addr2 := freeAddr(t)

	file := writeConfig(t, &config.DaemonConfig{
		TrackerURL: f.srv.URL,
		ListenAddr: addr2,
		BufferDir:  t.TempDir(),
	})
	d := newDaemon(&config.DaemonConfig{
		TrackerURL: f.srv.URL,
		ListenAddr: addr1,
		BufferDir:  t.TempDir(),
	}, file)
	defer d.Stop()

	if err := d.startHTTPServer(); err != nil {
		t.Fatalf("start http server: %v", err)
	}
	waitFor(t, "old addr to serve", func() bool {
		body, err := get("http://" + addr1 + "/ping")
		return err == nil && strings.Contains(body, `"status":"ok"`)
	})

	if err := d.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	waitFor(t, "new addr to serve", func() bool {
		body, err := get("http://" + addr2 + "/ping")
		return err == nil && strings.Contains(body, `"status":"ok"`)
	})
	waitFor(t, "old addr to close", func() bool {
		_, err := get("http://" + addr1 + "/ping")
		return err != nil
	})
}

// TestReloadTrackerChange (H2): a valid reload with a changed tracker_url
// re-announces to the new tracker.
func TestReloadTrackerChange(t *testing.T) {
	f1 := newFakeTracker(t)
	f2 := newFakeTracker(t)
	addr := freeAddr(t)

	file := writeConfig(t, &config.DaemonConfig{
		TrackerURL: f2.srv.URL,
		ListenAddr: addr,
		BufferDir:  t.TempDir(),
	})
	d := newDaemon(&config.DaemonConfig{
		TrackerURL: f1.srv.URL,
		ListenAddr: addr,
		BufferDir:  t.TempDir(),
	}, file)
	defer d.Stop()

	if err := d.startDiscovery(); err != nil {
		t.Fatalf("start discovery: %v", err)
	}
	if got := f1.announces.Load(); got != 1 {
		t.Fatalf("old tracker announces = %d, want 1", got)
	}

	if err := d.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := f2.announces.Load(); got != 1 {
		t.Fatalf("new tracker announces = %d, want 1", got)
	}
	if got := f1.announces.Load(); got != 1 {
		t.Fatalf("old tracker announces after reload = %d, want still 1", got)
	}
}

// TestReloadDropsCLIOverrides (H5): reload reads only the file, so a tracker_url
// that came from a CLI flag at startup is silently reverted to the file value.
func TestReloadDropsCLIOverrides(t *testing.T) {
	cliTracker := newFakeTracker(t)
	fileTracker := newFakeTracker(t)
	addr := freeAddr(t)

	file := writeConfig(t, &config.DaemonConfig{
		TrackerURL: fileTracker.srv.URL,
		ListenAddr: addr,
		BufferDir:  t.TempDir(),
	})
	// Startup config simulates `-tracker <cli>` winning over the file.
	d := newDaemon(&config.DaemonConfig{
		TrackerURL: cliTracker.srv.URL,
		ListenAddr: addr,
		BufferDir:  t.TempDir(),
	}, file)
	defer d.Stop()

	if err := d.startDiscovery(); err != nil {
		t.Fatalf("start discovery: %v", err)
	}
	if d.config.TrackerURL != cliTracker.srv.URL {
		t.Fatalf("setup: expected CLI override in effect, got %q", d.config.TrackerURL)
	}

	if err := d.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	// Reload reads only the file: the CLI override is dropped, not re-applied.
	if d.config.TrackerURL != fileTracker.srv.URL {
		t.Errorf("TrackerURL after reload = %q, want file value %q (CLI override dropped)", d.config.TrackerURL, fileTracker.srv.URL)
	}
}

// TestReloadInvalidFile (H3): an invalid file is rejected; the old settings stay
// in effect.
func TestReloadInvalidFile(t *testing.T) {
	f := newFakeTracker(t)
	addr := freeAddr(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// port 80 is below the valid range
	bad := fmt.Sprintf(`{"tracker_url":%q,"listen_addr":"127.0.0.1:80","buffer_dir":%q}`, f.srv.URL, dir)
	if err := os.WriteFile(path, []byte(bad), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d := newDaemon(&config.DaemonConfig{
		TrackerURL: f.srv.URL,
		ListenAddr: addr,
		BufferDir:  dir,
	}, path)

	if err := d.Reload(); err == nil {
		t.Fatal("reload with invalid file: want error, got nil")
	}
	if d.config.ListenAddr != addr {
		t.Errorf("ListenAddr after failed reload = %q, want old %q", d.config.ListenAddr, addr)
	}
}

// TestReloadCorruptFile (H4): a corrupt file is moved to .bak and defaults are
// applied (no tracker/server change since defaults equal current settings).
func TestReloadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	d := newDaemon(config.DefaultConfig(), path)

	if err := d.Reload(); err != nil {
		t.Fatalf("reload with corrupt file: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("expected %s to exist after corrupt reload", path+".bak")
	}
	if d.config.TrackerURL != "http://127.0.0.1:8080" || d.config.ListenAddr != "127.0.0.1:9001" {
		t.Errorf("expected defaults after corrupt reload, got %+v", d.config)
	}
}

// TestStartAnnounces (R8): Start() announces to the tracker (the "acknowledgement,
// peer registered" arrow) and serves the health endpoint.
func TestStartAnnounces(t *testing.T) {
	f := newFakeTracker(t)
	addr := freeAddr(t)
	cfg := &config.DaemonConfig{
		TrackerURL: f.srv.URL,
		ListenAddr: addr,
		BufferDir:  t.TempDir(),
	}

	if err := Start(cfg, "alice", []string{testCID}, filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer globalDaemon.Stop()

	if got := f.announces.Load(); got != 1 {
		t.Errorf("announces after Start = %d, want 1", got)
	}
	waitFor(t, "health endpoint to serve", func() bool {
		body, err := get("http://" + addr + "/ping")
		return err == nil && strings.Contains(body, `"status":"ok"`)
	})
}

// TestStop closes the servers and stops the heartbeat cleanly.
func TestStop(t *testing.T) {
	f := newFakeTracker(t)
	addr := freeAddr(t)
	cfg := &config.DaemonConfig{
		TrackerURL: f.srv.URL,
		ListenAddr: addr,
		BufferDir:  t.TempDir(),
	}
	if err := Start(cfg, "alice", []string{testCID}, filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, "health endpoint to serve", func() bool {
		_, err := get("http://" + addr + "/ping")
		return err == nil
	})

	if err := globalDaemon.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitFor(t, "server to close after Stop", func() bool {
		_, err := get("http://" + addr + "/ping")
		return err != nil
	})
}
