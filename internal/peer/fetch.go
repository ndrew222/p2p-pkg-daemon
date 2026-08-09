// Package peer is the fetch-and-seed half of the daemon: the daemon↔daemon
// wire for package bytes. Packages are addressed by name-version strings
// (e.g. "nginx-1.24.0_2"); there are no CIDs and no peer identities.
//
// Contract: docs/peer-transfer-spec-v0.2.md. Plain HTTP over TCP, GET
// /pkg/<name-version>, no TLS and no authentication -- integrity is end to end
// via pkg's repository database and nothing on this wire is trusted.
//
// NEITHER END HOLDS A PACKAGE IN MEMORY. The requester streams to a temporary
// file and hashes incrementally; the seeder serves from an open file handle. A
// []byte in either signature is the regression that OOMs a 1 GiB host on the
// 2.83 GiB package, and it is why FetchFromPeer returns an open *os.File
// rather than the bytes.
package peer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Transfer timeouts. There is deliberately NO bound on the body transfer: it
// would cap the whole download, so any package slower than the deadline would
// fail irrespective of size, and a 2.83 GiB transfer over a domestic uplink is
// legitimate traffic that no wall-clock rule can distinguish from a stall.
//
// A peer that sends headers and then trickles bytes forever is out of scope,
// exactly as a slow mirror is. Do not add a stall detector, a
// minimum-throughput rule or a transfer deadline. What bounds a hostile peer
// here is the exact expected size, not the clock.
const (
	dialTimeout           = 5 * time.Second
	responseHeaderTimeout = 10 * time.Second
)

var (
	ErrHashMismatch = errors.New("peer: downloaded bytes do not match the expected hash")
	ErrNoPeers      = errors.New("peer: no peer could serve the package")
	ErrPeerError    = errors.New("peer: remote returned an error")
	ErrBadName      = errors.New("peer: invalid package name-version")

	// ErrSizeMismatch is a breach of the size bound: a Content-Length that
	// disagrees with the repository database, or a body that runs past the
	// expected size.
	//
	// Abandoning is NOT blacklisting. The peer is dropped and the requester
	// moves to the next holder; only a hash mismatch marks it (UC-02 §11c).
	// A body of the wrong length fails the hash anyway if read to
	// completion, so a separate size verdict would be a second route to the
	// same conclusion.
	ErrSizeMismatch = errors.New("peer: transfer size does not match the repository database")

	// ErrSpool is a LOCAL failure of the temp directory, not a peer's
	// fault. It is distinguished so the fetch loop stops instead of
	// blaming every holder in turn, and so the facade can answer 5xx --
	// "this daemon is broken" -- rather than "no peer has it".
	ErrSpool = errors.New("peer: cannot spool the download to temp_dir")
)

// Want is the exact expectation a downloaded package must meet, read from
// pkg's repository database.
//
// Both fields come from the SAME row -- packages.cksum and packages.pkgsize --
// so any package that reaches the fetch path has both. An implementation that
// has one and not the other is a bug, not a case to handle gracefully.
//
// Size is what replaces peerwire.MaxPayload. It is per-package and exact, so
// it is strictly stronger than the retired 64 MiB constant -- a hostile peer
// cannot overrun by one byte -- while removing the ceiling entirely. The
// constant blocked 1.30% of the repository outright, including llvm, rust,
// chromium and libreoffice; do not reintroduce a global cap.
type Want struct {
	// Hash is the lowercase hex SHA-256 of the package file
	// (packages.cksum). Verified against 38,074 rows on the reference host.
	Hash string

	// Size is the exact file size in bytes (packages.pkgsize). Not
	// flatsize, which is the installed size and 2-20x larger.
	Size int64
}

// peerTransport is shared across fetches so idle connections are pooled and
// closed by the runtime rather than leaked per request.
//
// DisableCompression: a .pkg is already zstd-compressed, so offering
// Accept-Encoding would only invite a peer to re-encode it -- and a
// transparently decompressed body would break the size bound, because
// Content-Length would then describe the encoded length while the expected
// size describes the file.
var peerTransport = &http.Transport{
	DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
	ResponseHeaderTimeout: responseHeaderTimeout,
	DisableCompression:    true,
}

var peerClient = &http.Client{Transport: peerTransport}

// FetchFromPeer downloads nameVersion from one peer into a temporary file in
// tempDir, verifying it against want.
//
// On success it returns an OPEN, REWOUND *os.File whose contents are known to
// match want.Hash and want.Size exactly. The caller owns it and must both
// Close and Remove it -- the daemon has no store, and a spool that outlives
// its request is a cache nobody decided to build. On every failure path the
// temp file is closed and removed here, and nil is returned.
//
// Returning the file rather than the bytes is what carries the streaming
// guarantee up to the facade. A []byte return would silently reintroduce
// whole-package residency at the caller, which is exactly the defect this wire
// exists to fix.
func FetchFromPeer(ctx context.Context, addr, nameVersion string, want Want, tempDir string) (*os.File, error) {
	if !validName(nameVersion) {
		return nil, fmt.Errorf("peer: fetch: %w: %q", ErrBadName, nameVersion)
	}

	// url.URL escapes Path on the way out and net/http decodes it on the
	// way in, so the two ends agree on the identifier even when it carries
	// a character the path grammar would otherwise eat.
	u := url.URL{Scheme: "http", Host: addr, Path: packagePathPrefix + nameVersion}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("peer: fetch %s: %w", addr, err)
	}

	resp, err := peerClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("peer: fetch %s: %w", addr, err)
	}
	defer resp.Body.Close()

	// Every non-200 is the same to us: log, move to the next peer. The
	// distinctions (404 not held, 400 bad path, 405 wrong method, 503
	// capacity) are for an operator reading logs, so the code is reported
	// and not interpreted.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer: fetch %s: %w: HTTP %d", addr, ErrPeerError, resp.StatusCode)
	}

	// Half of the size bound, and the cheap half: a declared length that
	// disagrees with the repository database means abandoning the peer
	// BEFORE reading a single byte of body. resp.ContentLength is -1 when
	// the header is absent or the body is chunked, which is not an error --
	// the LimitReader below still bounds it.
	if resp.ContentLength >= 0 && resp.ContentLength != want.Size {
		return nil, fmt.Errorf("peer: fetch %s: %w: Content-Length %d, repository database says %d",
			addr, ErrSizeMismatch, resp.ContentLength, want.Size)
	}

	tmp, err := os.CreateTemp(tempDir, "jmj-*.pkg")
	if err != nil {
		return nil, fmt.Errorf("peer: fetch %q: %w: %v", nameVersion, ErrSpool, err)
	}
	// Removed on every failing path. Cleared once the file is handed to the
	// caller, who owns it from then on.
	keep := false
	defer func() {
		if !keep {
			tmp.Close()
			if err := os.Remove(tmp.Name()); err != nil && !os.IsNotExist(err) {
				log.Printf("peer: removing %s: %v", tmp.Name(), err)
			}
		}
	}()

	// The other half of the size bound, and the load-bearing half.
	// LimitReader at Size+1 means a peer streaming unbounded bytes gets
	// exactly one byte past the expected length before we stop reading; the
	// n != Size check below is what turns that extra byte into a rejection.
	// Hashing happens in the same pass, so nothing above the copy buffer
	// (~32 KiB) is ever resident.
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(resp.Body, want.Size+1))
	if err != nil {
		return nil, fmt.Errorf("peer: fetch %s: reading body: %w", addr, err)
	}
	if n != want.Size {
		return nil, fmt.Errorf("peer: fetch %s: %w: read %d bytes, repository database says %d",
			addr, ErrSizeMismatch, n, want.Size)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want.Hash {
		// The one failure that is evidence about the PEER rather than
		// about the network: it answered, it sent the right number of
		// bytes, and they are not the package. UC-02 §11c.
		return nil, fmt.Errorf("peer: fetch %s: %w: got %s, want %s", addr, ErrHashMismatch, got, want.Hash)
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("peer: fetch %q: %w: rewinding %s: %v", nameVersion, ErrSpool, tmp.Name(), err)
	}
	keep = true
	return tmp, nil
}

// validName is a MINIMAL sanity check, and deliberately so. The stricter
// structural rule -- a final hyphen separating a non-empty name from a
// digit-initial version -- is applied by the cache and facade layers, not
// here: a seeder that holds a file under some other name is free to serve it,
// and the requester's hash check is what actually decides.
//
// It is NOT a path-safety check. A name-version arriving off this wire may
// still contain separators as far as this function is concerned; any
// PackageSource that turns one into a filesystem path is responsible for
// refusing that itself.
func validName(s string) bool {
	if len(s) == 0 || len(s) > 255 {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}
