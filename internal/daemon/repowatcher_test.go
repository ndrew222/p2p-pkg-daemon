package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The watcher's whole job is timing, so every case here drives it with a
// settle delay short enough to keep the suite fast and long enough that a
// burst still lands inside one window on a loaded machine.
const testSettle = 60 * time.Millisecond

// reloadSpy records what the watcher asked for and lets a test wait for it.
type reloadSpy struct {
	mu     sync.Mutex
	calls  int
	nudges int
	err    error

	reloaded chan struct{}
	nudged   chan struct{}
}

func newReloadSpy() *reloadSpy {
	return &reloadSpy{
		reloaded: make(chan struct{}, 64),
		nudged:   make(chan struct{}, 64),
	}
}

func (s *reloadSpy) reload() error {
	s.mu.Lock()
	s.calls++
	err := s.err
	s.mu.Unlock()
	select {
	case s.reloaded <- struct{}{}:
	default:
	}
	return err
}

func (s *reloadSpy) onReload() {
	s.mu.Lock()
	s.nudges++
	s.mu.Unlock()
	select {
	case s.nudged <- struct{}{}:
	default:
	}
}

func (s *reloadSpy) failWith(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *reloadSpy) counts() (calls, nudges int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.nudges
}

func waitForSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// startRepoWatcher builds a watcher over dir with the test settle delay and
// stops it at the end of the case.
func startRepoWatcher(t *testing.T, dir string, spy *reloadSpy) *RepoWatcher {
	t.Helper()
	w := NewRepoWatcher(dir, spy.reload, spy.onReload)
	w.settle = testSettle
	if err := w.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	t.Cleanup(w.Stop)
	return w
}

func TestRepoWatcherReloadsWhenTheCatalogueChanges(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "FreeBSD-ports")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, repoDBFile), []byte("before"), 0644); err != nil {
		t.Fatal(err)
	}

	spy := newReloadSpy()
	startRepoWatcher(t, dir, spy)

	if err := os.WriteFile(filepath.Join(repoDir, repoDBFile), []byte("after"), 0644); err != nil {
		t.Fatal(err)
	}

	waitForSignal(t, spy.reloaded, "the reload")
	waitForSignal(t, spy.nudged, "the re-announce nudge")
}

// A catalogue refresh is not one write. pkg stages files and moves them into
// place, and reloading ~38,000 rows once per event would do the whole job
// several times over -- which is what the settle delay exists to prevent.
func TestRepoWatcherCollapsesABurstIntoOneReload(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "FreeBSD-ports")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	spy := newReloadSpy()
	startRepoWatcher(t, dir, spy)

	// Twenty events well inside one settle window.
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(repoDir, repoDBFile), []byte{byte(i)}, 0644); err != nil {
			t.Fatal(err)
		}
	}

	waitForSignal(t, spy.reloaded, "the reload")
	// Long enough that a second reload, if the coalescing were broken, would
	// have happened by now.
	time.Sleep(4 * testSettle)

	if calls, _ := spy.counts(); calls != 1 {
		t.Errorf("reloads = %d, want exactly 1 for one burst", calls)
	}
}

// HANDOFF §4.9, measured on the reference host: pkg touches <repo>/lock eleven
// seconds before it writes anything, so counting that event fires the settle
// timer inside the download and reloads the catalogue we already have.
func TestRepoWatcherIgnoresTheLockFile(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "FreeBSD-ports")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	spy := newReloadSpy()
	startRepoWatcher(t, dir, spy)

	// pkg taking the repository lock, on its own.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(repoDir, repoLockFile), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(4 * testSettle)
	if calls, _ := spy.counts(); calls != 0 {
		t.Errorf("reloads = %d after lock activity alone, want 0", calls)
	}

	// The catalogue itself still reloads, and the earlier lock events must
	// not have consumed the one reload it is owed.
	if err := os.WriteFile(filepath.Join(repoDir, repoDBFile), []byte("catalogue"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, spy.reloaded, "the reload for the catalogue write")

	time.Sleep(4 * testSettle)
	if calls, _ := spy.counts(); calls != 1 {
		t.Errorf("reloads = %d, want exactly 1 -- the catalogue write and nothing else", calls)
	}
}

// The real sequence, in order: lock, meta, a long silence, then the rewrite.
// Exactly one reload, and it must land after the catalogue is written rather
// than during the silence.
func TestRepoWatcherReloadsOncePerUpdateSequence(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "FreeBSD-ports")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	spy := newReloadSpy()
	startRepoWatcher(t, dir, spy)

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repoDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	write(repoLockFile, "")
	write("meta", "version 2")
	// The download: pkg writes nothing here for eleven seconds on the real
	// host, which is several settle delays.
	time.Sleep(3 * testSettle)

	// meta armed the timer, so one reload is expected by now and it is not
	// the spurious one -- meta really did change.
	afterMeta, _ := spy.counts()

	write(repoDBFile, "the new catalogue")
	waitForSignal(t, spy.reloaded, "the reload for the rewritten catalogue")
	time.Sleep(4 * testSettle)

	total, _ := spy.counts()
	if total != afterMeta+1 {
		t.Errorf("reloads = %d, want %d: the rewrite is owed exactly one more than meta already caused",
			total, afterMeta+1)
	}
	if total > 2 {
		t.Errorf("reloads = %d for one update sequence, want at most 2", total)
	}
}

// ADR-008: a runtime reload failure logs and keeps the previous catalogue. It
// must not nudge -- nothing about what this host can serve has changed -- and
// it must not stop the watcher, or one transient error would leave the daemon
// stale for the rest of the process.
func TestRepoWatcherSurvivesAFailedReloadAndDoesNotNudge(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "FreeBSD-ports")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	spy := newReloadSpy()
	spy.failWith(errors.New("catalogue is half-written"))
	startRepoWatcher(t, dir, spy)

	if err := os.WriteFile(filepath.Join(repoDir, repoDBFile), []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, spy.reloaded, "the failing reload")

	if _, nudges := spy.counts(); nudges != 0 {
		t.Errorf("nudges = %d after a failed reload, want 0", nudges)
	}

	// The next rewrite still gets a reload, and this one succeeds.
	spy.failWith(nil)
	if err := os.WriteFile(filepath.Join(repoDir, repoDBFile), []byte("two"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, spy.reloaded, "the second reload")
	waitForSignal(t, spy.nudged, "the re-announce nudge after recovery")
}

// The watch set is rebuilt after every reload, which is what lets a repository
// that appears later be watched at all -- and, on kqueue, what heals a watch
// lost to a rename.
func TestRepoWatcherPicksUpARepositoryAddedAfterStart(t *testing.T) {
	dir := t.TempDir()

	spy := newReloadSpy()
	startRepoWatcher(t, dir, spy)

	// Creating the directory is itself an event on the root, so it produces
	// a reload; that reload is what adds the watch on the new directory.
	newRepo := filepath.Join(dir, "FreeBSD-ports-kmods")
	if err := os.MkdirAll(newRepo, 0755); err != nil {
		t.Fatal(err)
	}
	// Waiting on the nudge, not the reload: reloadNow signals the reload
	// before it rebuilds the watch set, so a test that wrote on that signal
	// would race the Add it is trying to observe. The nudge fires after.
	waitForSignal(t, spy.nudged, "the reload for the new repository directory")

	// A write INSIDE the new directory is only seen if the re-walk added it.
	if err := os.WriteFile(filepath.Join(newRepo, repoDBFile), []byte("db"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, spy.reloaded, "the reload for a write inside the new repository")
}

// repo_db_dir holds pkg's signed catalogues and is read-only to this daemon.
// A missing directory is refused, never created -- the same hard constraint the
// cache watcher violated once with MkdirAll.
func TestRepoWatcherStartRefusesAndCreatesNothing(t *testing.T) {
	parent := t.TempDir()

	notADir := filepath.Join(parent, "a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		dir  string
	}{
		{"missing directory", filepath.Join(parent, "does-not-exist")},
		{"not a directory", notADir},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := NewRepoWatcher(tc.dir, func() error { return nil }, nil)
			if err := w.Start(); err == nil {
				w.Stop()
				t.Fatal("Start() = nil, want an error")
			}
		})
	}

	// Nothing was created, and the file is still a file.
	if _, err := os.Stat(filepath.Join(parent, "does-not-exist")); !os.IsNotExist(err) {
		t.Errorf("Start created the missing directory (stat err = %v)", err)
	}
	info, err := os.Stat(notADir)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Error("Start replaced the file with a directory")
	}
}

func TestRepoWatcherStopIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	w := NewRepoWatcher(dir, func() error { return nil }, nil)
	w.settle = testSettle
	if err := w.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	w.Stop()
	w.Stop()
}

// A watcher that was never started must still be safe to stop: startDiscovery
// can fail between constructing one and starting it.
func TestRepoWatcherStopWithoutStart(t *testing.T) {
	NewRepoWatcher(t.TempDir(), func() error { return nil }, nil).Stop()
}
