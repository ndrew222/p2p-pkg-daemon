package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// repoSettleDelay is how long the watcher waits for repo_db_dir to go quiet
// before reloading (ADR-008).
//
// A catalogue refresh is not one write: pkg stages files and moves them into
// place, and each step is one or more events. Reloading on the first would read
// a half-written catalogue; reloading on each would do the whole ~38,000-row
// job several times over.
//
// Two seconds, and deliberately not a config key. The delay costs nothing that
// can be observed: the only way it could matter is an install starting inside
// the window, and `pkg install` cannot begin before the `pkg update` that
// produced the events has finished.
//
// A var rather than a const only so the tests need not wait two seconds a
// case. Nothing outside a test writes it.
var repoSettleDelay = 2 * time.Second

// RepoWatcher reloads the repository database when pkg rewrites it (ADR-008,
// HANDOFF §5.2).
//
// It watches DIRECTORIES, never the db files themselves. On kqueue a watch on
// <repo>/db follows the inode, so a catalogue replaced by a rename would leave
// the watch pointing at a file nothing will ever write to again -- silently,
// and for the life of the process. A directory watch sees the rename.
//
// repo_db_dir is pkg's own signed catalogue directory and is READ-ONLY to this
// daemon. A missing directory is refused, never created; the cache watcher's
// MkdirAll was a hard-constraint violation and this applies with more force.
type RepoWatcher struct {
	dir string
	// reload swaps the snapshot. It is the only thing this type does with
	// the catalogue, so tests can drive it without a real SQLite file.
	reload func() error
	// onReload fires after a SUCCESSFUL reload and nothing else. It is the
	// re-announce nudge: SanityFilter has been comparing cached files
	// against superseded sizes, and a file it dropped will never be
	// revisited by a cache event, because nothing about that file changed.
	onReload func()
	// settle is repoSettleDelay in production. A field so tests do not have
	// to wait two seconds a case.
	settle time.Duration

	watcher  *fsnotify.Watcher
	done     chan struct{}
	stopOnce sync.Once
}

// NewRepoWatcher creates the watcher. The caller should call Start.
//
// reload is required; onReload may be nil.
func NewRepoWatcher(dir string, reload func() error, onReload func()) *RepoWatcher {
	return &RepoWatcher{
		dir:      dir,
		reload:   reload,
		onReload: onReload,
		settle:   repoSettleDelay,
		done:     make(chan struct{}),
	}
}

// Start begins watching repo_db_dir.
func (w *RepoWatcher) Start() error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify new watcher: %w", err)
	}
	w.watcher = fw

	if err := w.addWatches(); err != nil {
		fw.Close()
		w.watcher = nil
		return err
	}

	go w.loop()
	return nil
}

// Stop shuts the watcher down. Safe to call more than once.
func (w *RepoWatcher) Stop() {
	w.stopOnce.Do(func() {
		close(w.done)
		if w.watcher != nil {
			w.watcher.Close()
		}
	})
}

// addWatches rebuilds the watch set from the directory tree.
//
// Called by Start and again after every reload attempt, which is what makes the
// design correct on inotify and kqueue alike: a repository directory can
// appear, vanish or be replaced wholesale, and rather than reason about which
// of those each platform reports as which event, the set is simply rebuilt.
// fsnotify.Add is idempotent, so re-adding a live watch costs nothing.
func (w *RepoWatcher) addWatches() error {
	info, err := os.Stat(w.dir)
	if err != nil {
		return fmt.Errorf("repo db dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo db dir %q is not a directory", w.dir)
	}

	return filepath.Walk(w.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// A directory that went away mid-walk is exactly the event
			// this watcher exists to notice, not a reason to stop
			// watching everything else.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			return nil
		}
		return w.watcher.Add(path)
	})
}

func (w *RepoWatcher) loop() {
	// timer is nil whenever no reload is pending. Each event installs a
	// fresh one rather than resetting the old: a Timer that has already
	// fired holds a value in its own channel, and replacing the channel we
	// select on makes that value unreachable instead of something to drain.
	var timer *time.Timer
	var fire <-chan time.Time

	for {
		select {
		case <-w.done:
			if timer != nil {
				timer.Stop()
			}
			return

		case _, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			// Every event under repo_db_dir counts, whatever its op. The
			// watcher does not try to tell a catalogue rewrite from a
			// journal file being created -- the reload reads the whole
			// tree anyway, and the settle delay is what makes guessing
			// unnecessary.
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(w.settle)
			fire = timer.C

		case <-fire:
			timer, fire = nil, nil
			w.reloadNow()

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[repowatcher] fsnotify error: %v", err)
		}
	}
}

// reloadNow performs the reload and rebuilds the watch set.
//
// A failure here is NOT fatal and does NOT discard the snapshot (ADR-008).
// Failure at startup is fatal -- openRepositoriesLocked's reasoning is
// unchanged -- but at runtime the daemon has a working catalogue and the
// alternative to keeping a stale one is having none. Repositories.Reload builds
// its replacement maps in locals and returns before swapping, so the previous
// rows survive a failed attempt; this relies on that and daemon_test pins it.
func (w *RepoWatcher) reloadNow() {
	err := w.reload()

	// Rebuilt whether or not the reload succeeded: a reload that failed
	// because a directory was mid-rename is precisely when the watch set is
	// most likely to be stale.
	if addErr := w.addWatches(); addErr != nil {
		log.Printf("repowatcher: %s: cannot re-establish the watches: %v", w.dir, addErr)
	}

	if err != nil {
		log.Printf("repowatcher: %s changed but the reload failed; keeping the previous catalogue: %v", w.dir, err)
		return
	}
	if w.onReload != nil {
		w.onReload()
	}
}
