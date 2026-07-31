package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ndrew222/p2p-pkg-daemon/internal/peer"
)

// --- test doubles -----------------------------------------------------------

type fakeLister struct {
	addrs []string
	err   error
}

func (f fakeLister) Peers(string) ([]string, error) { return f.addrs, f.err }

type fakeHashes map[string]string

func (f fakeHashes) ExpectedHash(nameVersion string) (string, bool) {
	h, ok := f[nameVersion]
	return h, ok
}

type fakeCache map[string][]byte

func (f fakeCache) Get(nameVersion string) ([]byte, bool) {
	b, ok := f[nameVersion]
	return b, ok
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// startPeer runs a real peer.Server on a random port and returns its address.
// The 200 path is worth exercising end to end: it is the only one that proves
// the facade, the fetch loop and hash verification agree.
func startPeer(t *testing.T, cache fakeCache) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Deliberately NOT closed on cleanup: peer.Server.Serve spins on a closed
	// listener (it logs and retries every Accept error, including the
	// permanent one), so closing here burns a core and floods the test log.
	// Leaking the listener until the test binary exits is the lesser evil
	// until that loop is fixed. See the work log.
	srv := &peer.Server{Source: cache}
	go srv.Serve(ln)
	return ln.Addr().String()
}

// --- path classification ----------------------------------------------------

func TestPackageRequest(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantName    string
		wantPackage bool
	}{
		// The worked example from the spec, and the prefix-invariance that
		// makes the tail-match rule work across mirrors/ABIs/branches.
		{"ghostbsd example", "/stable/FreeBSD:15:amd64/latest/All/gopls-0.22.0_1.pkg", "gopls-0.22.0_1", true},
		{"bare All prefix", "/All/nginx-1.24.0_2.pkg", "nginx-1.24.0_2", true},
		{"deep repo path", "/a/b/c/d/e/All/curl-8.6.0.pkg", "curl-8.6.0", true},
		{"hyphenated name", "/All/py311-setuptools-63.1.0.pkg", "py311-setuptools-63.1.0", true},

		// UC-07: everything that is not a package file.
		{"meta.conf", "/stable/FreeBSD:15:amd64/latest/meta.conf", "", false},
		{"packagesite outside All", "/stable/FreeBSD:15:amd64/latest/packagesite.pkg", "", false},
		{"data.pkg outside All", "/latest/data.pkg", "", false},
		{"root", "/", "", false},
		{"All directory itself", "/latest/All/", "", false},
		{"non-pkg file under All", "/latest/All/README.txt", "", false},
		{"All not the parent", "/All/nested/nginx-1.24.0_2.pkg", "", false},

		// Shaped like a package request, but the stem is not a name-version.
		{"no version", "/All/nginx.pkg", "", true},
		{"version not digit-initial", "/All/nginx-latest.pkg", "", true},
		{"empty stem", "/All/.pkg", "", true},

		// Traversal must not smuggle All into the parent position.
		{"traversal", "/All/../etc/passwd", "", false},
		{"traversal to All", "/foo/../All/nginx-1.24.0_2.pkg", "nginx-1.24.0_2", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := packageRequest(tc.path)
			if ok != tc.wantPackage {
				t.Fatalf("packageRequest(%q) ok = %v, want %v", tc.path, ok, tc.wantPackage)
			}
			if got != tc.wantName {
				t.Errorf("packageRequest(%q) = %q, want %q", tc.path, got, tc.wantName)
			}
		})
	}
}

// --- status codes -----------------------------------------------------------

func TestFacadeStatusCodes(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	const pkgPath = "/stable/FreeBSD:15:amd64/latest/All/nginx-1.24.0_2.pkg"
	content := []byte("package bytes")

	goodPeer := startPeer(t, fakeCache{pkgName: content})
	// A peer that answers but with the wrong bytes: hash mismatch (UC-02 9c).
	corruptPeer := startPeer(t, fakeCache{pkgName: []byte("tampered")})
	// A peer that is not listening at all: connection failure (UC-02 8e).
	deadPeer := "127.0.0.1:1"

	hashes := fakeHashes{pkgName: sha256Hex(content)}

	tests := []struct {
		name     string
		method   string
		path     string
		lister   peer.PeerLister
		hashes   PackageHashes
		wantCode int
	}{
		{
			name: "verified download", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, hashes: hashes,
			wantCode: http.StatusOK,
		},
		{
			name: "falls through to a later peer", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{deadPeer, corruptPeer, goodPeer}}, hashes: hashes,
			wantCode: http.StatusOK,
		},
		{
			name: "metadata path", method: http.MethodGet, path: "/stable/FreeBSD:15:amd64/latest/meta.conf",
			lister: fakeLister{addrs: []string{goodPeer}}, hashes: hashes,
			wantCode: http.StatusNotFound,
		},
		{
			name: "malformed name-version", method: http.MethodGet, path: "/latest/All/nginx.pkg",
			lister: fakeLister{addrs: []string{goodPeer}}, hashes: hashes,
			wantCode: http.StatusBadRequest,
		},
		{
			name: "not in repository database", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, hashes: fakeHashes{},
			wantCode: http.StatusNotFound,
		},
		{
			name: "no repository database wired up", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, hashes: nil,
			wantCode: http.StatusNotFound,
		},
		{
			name: "tracker returned no peers", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: nil}, hashes: hashes,
			wantCode: http.StatusNotFound,
		},
		{
			name: "tracker unreachable", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{err: errors.New("dial tcp: connection refused")}, hashes: hashes,
			wantCode: http.StatusBadGateway,
		},
		{
			name: "all peers exhausted", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{deadPeer, corruptPeer}}, hashes: hashes,
			wantCode: http.StatusBadGateway,
		},
		{
			name: "HEAD is not served", method: http.MethodHead, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, hashes: hashes,
			wantCode: http.StatusMethodNotAllowed,
		},
		{
			name: "POST is not served", method: http.MethodPost, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, hashes: hashes,
			wantCode: http.StatusMethodNotAllowed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &Facade{Peers: tc.lister, Hashes: tc.hashes}
			rec := httptest.NewRecorder()
			f.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if rec.Code != http.StatusOK {
				return
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("Content-Type = %q, want application/octet-stream", ct)
			}
			body, _ := io.ReadAll(rec.Body)
			if string(body) != string(content) {
				t.Errorf("body = %q, want %q", body, content)
			}
		})
	}
}

// A corrupt peer must never have its bytes reach pkg, even when it is the only
// peer available -- the whole point of end-to-end verification.
func TestFacadeNeverServesUnverifiedBytes(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	content := []byte("package bytes")
	corruptPeer := startPeer(t, fakeCache{pkgName: []byte("tampered")})

	f := &Facade{
		Peers:  fakeLister{addrs: []string{corruptPeer}},
		Hashes: fakeHashes{pkgName: sha256Hex(content)},
	}
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/latest/All/nginx-1.24.0_2.pkg", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if body, _ := io.ReadAll(rec.Body); string(body) == "tampered" {
		t.Fatal("facade served unverified bytes to pkg")
	}
}

func TestFacadeCheck(t *testing.T) {
	if err := (&Facade{}).Check(); err == nil {
		t.Error("Check() on an empty facade = nil, want an error")
	}
	f := &Facade{Peers: fakeLister{}}
	if err := f.Check(); !errors.Is(err, ErrNoRepositoryDatabase) {
		t.Errorf("Check() without hashes = %v, want ErrNoRepositoryDatabase", err)
	}
	f.Hashes = fakeHashes{}
	if err := f.Check(); err != nil {
		t.Errorf("Check() on a wired facade = %v, want nil", err)
	}
}
