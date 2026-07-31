package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/fsnotify/fsnotify"
)

// packageFileExtension is the file extension pkgng uses for package
// archives in the local cache (/var/cache/pkg). FreeBSD's pkgng uses
// ".pkg" for its packages (older setups sometimes use ".txz" instead).
//
// TODO: confirm this against a real pkg cache directory / with Andrew.
// If your cache directory actually uses ".txz"
const packageFileExtension = ".pkg"

// PackageInfo represents a discovered package in the cache directory.
type PackageInfo struct {
	// Name is the package name, e.g. "nginx" 
	Name string `json:"name"`

	// Version is the version string, e.g. "1.24.0"
	Version string `json:"version"`

	// Path is the full path to the file on disk, inside the pkg cache directory
	Path string `json:"path"`

	// FileSizeBytes is the size of the file on disk, in bytes. This is
	// what SanityFilter compares against the repository database's
	// expected size 
	FileSizeBytes int64 `json:"file_size_bytes"`
}

// NameVersion returns the package identifier in "name-version" form (e.g.
// "nginx-1.24.0"), which is the exact string format the tracker and pkg
// itself use to refer to packages. If Version is empty (the file name had
// no recognizable version), this just returns Name
func (p PackageInfo) NameVersion() string {
	if p.Version == "" {
		return p.Name
	}
	return p.Name + "-" + p.Version
}

// RepositoryDatabase is the thing that knows the "expected" facts about a
// package, as recorded in pkg's signed repository database. SanityFilter
// uses it clarify the expected size of a package file on disk
type RepositoryDatabase interface {
	// ExpectedFileSizeBytes looks up the expected size, in bytes
	ExpectedFileSizeBytes(nameVersion string) (expectedSizeBytes int64, found bool)
}

// ChangeType describes what happened to a package
type ChangeType int

const (
	Added ChangeType = iota
	Removed
	Modified
)

func (c ChangeType) String() string {
	switch c {
	case Added:
		return "added"
	case Removed:
		return "removed"
	case Modified:
		return "modified"
	default:
		return "unknown"
	}
}

// ChangeEvent describes a single package change
type ChangeEvent struct {
	Type    ChangeType
	Package PackageInfo
}

// Watcher monitors the cache directory for package changes
type Watcher struct {
	cacheDir string
	repoDB   RepositoryDatabase
	onUpdate func([]PackageInfo)
	onChange func(ChangeEvent)
	watcher  *fsnotify.Watcher
	mu       sync.RWMutex
	pkgs     map[string]PackageInfo
	done     chan struct{}
	stopOnce sync.Once
}

// New creates a new cache watcher. It returns *Watcher, not an error. The
// caller should call Start() after construction.
//
// repoDB is used to sanity-check every discovered package's file size
// against what the repository database expects (see SanityFilter). Pass a
// real implementation backed by pkg's repo DB in production; tests can pass
// a small in-memory fake instead.
//
// onChange is optional; pass nil if you don't need per-event notifications.
func New(cacheDir string, repoDB RepositoryDatabase, onUpdate func([]PackageInfo), onChange func(ChangeEvent)) *Watcher {
	return &Watcher{
		cacheDir: cacheDir,
		repoDB:   repoDB,
		onUpdate: onUpdate,
		onChange: onChange,
		pkgs:     make(map[string]PackageInfo),
		done:     make(chan struct{}),
	}
}

// Start begins watching the cache directory. Returns an error if fsnotify fails.
func (w *Watcher) Start() error {
	if err := os.MkdirAll(w.cacheDir, 0755); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify new watcher: %w", err)
	}
	w.watcher = fw

	if err := filepath.Walk(w.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return w.watcher.Add(path)
		}
		return nil
	}); err != nil {
		fw.Close()
		return fmt.Errorf("walk cache dir: %w", err)
	}

	go w.loop()
	return nil
}

// Stop shuts down the watcher
func (w *Watcher) Stop() {
	w.stopOnce.Do(func() {
		close(w.done)
		if w.watcher != nil {
			w.watcher.Close()
		}
	})
}

// parsePackageName extracts the name and version from a file name.

func parsePackageName(filename string) (name, version string) {
	base := strings.TrimSuffix(filename, packageFileExtension)

	lastHyphenIndex := strings.LastIndex(base, "-")
	if lastHyphenIndex == -1 {
		return base, ""
	}

	candidateName := base[:lastHyphenIndex]
	candidateVersion := base[lastHyphenIndex+1:]

	if candidateVersion == "" || !startsWithDigit(candidateVersion) {
		return base, ""
	}

	return candidateName, candidateVersion
}

func startsWithDigit(s string) bool {
	if s == "" {
		return false
	}
	firstRune := []rune(s)[0]
	return unicode.IsDigit(firstRune)
}

func isValidNameVersionFormat(pkg PackageInfo) bool {
	return pkg.Name != "" && pkg.Version != ""
}

func SanityFilter(candidates []PackageInfo, repoDB RepositoryDatabase) []PackageInfo {
	accepted := make([]PackageInfo, 0, len(candidates))

	for _, candidate := range candidates {
		if !isValidNameVersionFormat(candidate) {
			continue
		}

		if repoDB != nil {
			expectedSizeBytes, foundInRepoDB := repoDB.ExpectedFileSizeBytes(candidate.NameVersion())
			if !foundInRepoDB {
				// The repository database has never heard of this
				// package. Skip it.
				continue
			}
			if candidate.FileSizeBytes != expectedSizeBytes {
				continue
			}
		}

		accepted = append(accepted, candidate)
	}

	return accepted
}

// Scan forces a full rescan of the cache directory, applies the sanity
// filter, and emits an update with the resulting package list.
func (w *Watcher) Scan() ([]PackageInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pkgs = make(map[string]PackageInfo)
	err := filepath.Walk(w.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		filename := filepath.Base(path)
		name, ver := parsePackageName(filename)

		w.pkgs[path] = PackageInfo{
			Name:          name,
			Version:       ver,
			Path:          path,
			FileSizeBytes: info.Size(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	rawCandidates := make([]PackageInfo, 0, len(w.pkgs))
	for _, p := range w.pkgs {
		rawCandidates = append(rawCandidates, p)
	}

	out := SanityFilter(rawCandidates, w.repoDB)
	if w.onUpdate != nil {
		w.onUpdate(out)
	}
	return out, nil
}

func (w *Watcher) loop() {
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == fsnotify.Create {
				w.handleEvent(event.Name, Added)
			} else if event.Op&fsnotify.Write == fsnotify.Write {
				w.handleEvent(event.Name, Modified)
			} else if event.Op&fsnotify.Remove == fsnotify.Remove {
				w.handleEvent(event.Name, Removed)
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "[cachewatcher] fsnotify error: %v\n", err)
		}
	}
}

func (w *Watcher) handleEvent(path string, changeType ChangeType) {
	filename := filepath.Base(path)
	name, ver := parsePackageName(filename)
	var fileSizeBytes int64
	if changeType != Removed {
		if info, err := os.Stat(path); err == nil {
			fileSizeBytes = info.Size()
		}
	}

	pkg := PackageInfo{
		Name:          name,
		Version:       ver,
		Path:          path,
		FileSizeBytes: fileSizeBytes,
	}

	if w.onChange != nil {
		w.onChange(ChangeEvent{
			Type:    changeType,
			Package: pkg,
		})
	}

	if _, err := w.Scan(); err != nil {
		fmt.Fprintf(os.Stderr, "[cachewatcher] rescan after change failed: %v\n", err)
	}
}
