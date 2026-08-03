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

const packageFileExtension = ".pkg"

// PackageInfo describes a package file found in the pkg cache.
type PackageInfo struct {
	// Name is the package name, e.g. "nginx"
	Name string `json:"name"`

	Version string `json:"version"`

	Path string `json:"path"`

	FileSizeBytes int64 `json:"file_size_bytes"`
}

// NameVersion returns the package identifier as "name-version".
func (p PackageInfo) NameVersion() string {
	if p.Version == "" {
		return p.Name
	}
	return p.Name + "-" + p.Version
}

// RepositoryDatabase looks up expected package sizes from pkg's repo DB
type RepositoryDatabase interface {
	ExpectedFileSizeBytes(nameVersion string) (expectedSizeBytes int64, found bool)
}

// ChangeType describes what happened to a package
type ChangeType int

// Added, Removed, and Modified are the possible ChangeType values.
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
	cacheDir      string
	listeningPort int
	repoDB        RepositoryDatabase
	onUpdate      func(listeningPort int, pkgs []PackageInfo)
	onChange      func(ChangeEvent)
	watcher       *fsnotify.Watcher
	mu            sync.RWMutex
	pkgs          map[string]PackageInfo
	done          chan struct{}
	stopOnce      sync.Once
}

// New creates a Watcher for the given cache directory.
func New(cacheDir string, listeningPort int, repoDB RepositoryDatabase, onUpdate func(listeningPort int, pkgs []PackageInfo), onChange func(ChangeEvent)) *Watcher {
	return &Watcher{
		cacheDir:      cacheDir,
		listeningPort: listeningPort,
		repoDB:        repoDB,
		onUpdate:      onUpdate,
		onChange:      onChange,
		pkgs:          make(map[string]PackageInfo),
		done:          make(chan struct{}),
	}
}

// Start begins watching the cache directory for changes.
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

// Stop shuts down the watcher. Safe to call more than once.
func (w *Watcher) Stop() {
	w.stopOnce.Do(func() {
		close(w.done)
		if w.watcher != nil {
			w.watcher.Close()
		}
	})
}

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

// SanityFilter keeps only packages with a valid name-version filename
// and a file size matching the repository database.
func SanityFilter(candidates []PackageInfo, repoDB RepositoryDatabase) []PackageInfo {
	accepted := make([]PackageInfo, 0, len(candidates))

	for _, candidate := range candidates {
		if !isValidNameVersionFormat(candidate) {
			continue
		}

		if repoDB != nil {
			expectedSizeBytes, foundInRepoDB := repoDB.ExpectedFileSizeBytes(candidate.NameVersion())
			if !foundInRepoDB {
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

// Scan rescans the cache directory and reports the full, filtered
// package list via onUpdate.
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
		w.onUpdate(w.listeningPort, out)
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
