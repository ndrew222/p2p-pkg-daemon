package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ndrew222/p2p-pkg-daemon/internal/peer"
)

// --- test doubles -----------------------------------------------------------

type fakeLister struct {
	addrs []string
	err   error
}

func (f fakeLister) Peers(string) ([]string, error) { return f.addrs, f.err }

// fakeRepo is a Repository: both views of one repository-database row, keyed by
// name-version. A row carries a hash and a size because the real database does
// -- both columns are NOT NULL -- so a fake that could supply one without the
// other would let a test pass in a state production cannot reach.
type fakeRepo map[string]fakeRow

type fakeRow struct {
	hash string
	size int64
}

func (f fakeRepo) ExpectedHash(nameVersion string) (string, bool) {
	r, ok := f[nameVersion]
	return r.hash, ok
}

func (f fakeRepo) ExpectedFileSizeBytes(nameVersion string) (int64, bool) {
	r, ok := f[nameVersion]
	return r.size, ok
}

// fakeCache is a peer.PackageSource for the facade's tests: it hands back an
// open handle over an in-memory reader.
//
// A test double may hold bytes; the production source may not. This one exists
// so the facade's tests do not have to build a directory tree per case, and the
// constant-memory guarantee is asserted where it lives, in internal/peer.
type fakeCache map[string][]byte

func (f fakeCache) Open(nameVersion string) (io.ReadSeekCloser, int64, bool) {
	b, ok := f[nameVersion]
	if !ok {
		return nil, 0, false
	}
	return nopSeekCloser{bytes.NewReader(b)}, int64(len(b)), true
}

type nopSeekCloser struct{ io.ReadSeeker }

func (nopSeekCloser) Close() error { return nil }

// tamperedLike returns bytes that are the SAME LENGTH as b and not b.
//
// The length matters. Under the v0.2 wire a body of the wrong length is caught
// by the size bound, which abandons the peer WITHOUT blacklisting it -- the
// size is a bound, not a verdict. Only a hash mismatch is evidence about the
// peer (UC-02 §11c), and it is only reachable at the right length. A shorter
// "tampered" string would therefore test the size bound while appearing to
// test verification, and every blacklist assertion here would fail for a
// reason that has nothing to do with the facade.
func tamperedLike(b []byte) []byte {
	return bytes.Repeat([]byte("X"), len(b))
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// startPeer runs a real peer.Server on a random port and returns its address in
// the host:port form the tracker hands out. The 200 path is worth exercising
// end to end: it is the only one that proves the facade, the fetch loop and
// hash verification agree.
func startPeer(t *testing.T, cache fakeCache) string {
	t.Helper()
	ts := httptest.NewServer(&peer.Server{Source: cache})
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

// upstreamStub stands in for the configured upstream mirror (ADR-006's
// upstream_url). Everything the facade cannot answer from a peer goes here, so
// most of the contract is now only observable as "what did upstream see, and
// what came back".
//
// It records requests as well as answering them because two of the narrowed
// rules are about upstream NOT being asked: a 404 for a package absent from the
// repository database, and a 400 for a malformed name-version, are both
// answered without a fetch.
type upstreamStub struct {
	URL string

	mu  sync.Mutex
	got []upstreamRequest
}

type upstreamRequest struct {
	Method string
	Path   string
	Query  string
	IMS    string
}

func startUpstream(t *testing.T, h http.HandlerFunc) *upstreamStub {
	t.Helper()
	u := &upstreamStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.got = append(u.got, upstreamRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			IMS:    r.Header.Get("If-Modified-Since"),
		})
		u.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	u.URL = srv.URL
	return u
}

func (u *upstreamStub) requests() []upstreamRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]upstreamRequest(nil), u.got...)
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

		// What pkg 2.7.5 actually requests, measured on FreeBSD
		// 15.1-RELEASE-p1. The Hashed/ level and the ~hash10 suffix are
		// both present, and the earlier rule 404'd every one of these.
		{
			"pkg 2.7.5 hashed request",
			"/stable/FreeBSD:15:amd64/latest/All/Hashed/indexinfo-0.3.1_1~ae9dce33aa.pkg",
			"indexinfo-0.3.1_1", true,
		},
		{"hashed without suffix", "/latest/All/Hashed/curl-8.6.0.pkg", "curl-8.6.0", true},
		{"suffix without Hashed", "/latest/All/curl-8.6.0~0123456789.pkg", "curl-8.6.0", true},
		{
			"hashed hyphenated name",
			"/All/Hashed/py311-setuptools-63.1.0~abcdef0123.pkg",
			"py311-setuptools-63.1.0", true,
		},

		// The suffix rule is exactly 10 lowercase hex after a tilde.
		// Anything else is part of the version, not a checksum, and must
		// not be silently eaten.
		{"suffix too short", "/All/curl-8.6.0~abc.pkg", "curl-8.6.0~abc", true},
		{"suffix not hex", "/All/curl-8.6.0~zzzzzzzzzz.pkg", "curl-8.6.0~zzzzzzzzzz", true},
		{"suffix uppercase", "/All/curl-8.6.0~ABCDEF0123.pkg", "curl-8.6.0~ABCDEF0123", true},

		// Hashed is tolerated directly under All and nowhere else.
		{"Hashed without All", "/latest/Hashed/curl-8.6.0.pkg", "", false},
		{"Hashed too deep", "/All/Hashed/more/curl-8.6.0.pkg", "", false},
		{"Hashed directory itself", "/All/Hashed/", "", false},

		// A repo directory that happens to be named All does not displace
		// the real one: the last All wins.
		{"repo named All", "/All/latest/All/curl-8.6.0.pkg", "curl-8.6.0", true},
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

// --- joining a client-supplied path onto the upstream base ------------------

// ADR-005: the proxied path comes from the client, so the join must not permit
// escaping the base. This is the facade's new attack surface and the only place
// it is defended.
func TestUpstreamURL(t *testing.T) {
	const base = "https://pkg.example.org/FreeBSD:15:amd64/quarterly"

	tests := []struct {
		name  string
		base  string
		path  string
		query string
		want  string
	}{
		{
			name: "ordinary package path", base: base,
			path: "/All/nginx-1.24.0_2.pkg",
			want: "https://pkg.example.org/FreeBSD:15:amd64/quarterly/All/nginx-1.24.0_2.pkg",
		},
		{
			// The ABI segment carries colons and must survive re-escaping,
			// or every real request goes to the wrong URL.
			name: "colons in the base survive", base: base,
			path: "/meta.conf",
			want: "https://pkg.example.org/FreeBSD:15:amd64/quarterly/meta.conf",
		},
		{
			name: "root", base: base, path: "/",
			want: "https://pkg.example.org/FreeBSD:15:amd64/quarterly/",
		},
		{
			name: "base with a trailing slash does not double it",
			base: base + "/", path: "/meta.conf",
			want: "https://pkg.example.org/FreeBSD:15:amd64/quarterly/meta.conf",
		},
		{
			name: "base with no path", base: "http://127.0.0.1:9999",
			path: "/All/curl-8.6.0.pkg",
			want: "http://127.0.0.1:9999/All/curl-8.6.0.pkg",
		},
		{
			name: "query is relayed", base: base, path: "/meta.conf", query: "a=1&b=2",
			want: "https://pkg.example.org/FreeBSD:15:amd64/quarterly/meta.conf?a=1&b=2",
		},

		// Containment. Every one of these must land under the base.
		{
			name: "dot-dot cannot climb out of the base", base: base,
			path: "/../../../../etc/passwd",
			want: "https://pkg.example.org/FreeBSD:15:amd64/quarterly/etc/passwd",
		},
		{
			name: "dot-dot in the middle", base: base,
			path: "/All/../../../secret",
			want: "https://pkg.example.org/FreeBSD:15:amd64/quarterly/secret",
		},
		{
			// net/http has already decoded the path by the time the facade
			// sees it, so this is what %2e%2e%2f arrives as.
			name: "decoded percent-encoded traversal", base: base,
			path: "/../etc/passwd",
			want: "https://pkg.example.org/FreeBSD:15:amd64/quarterly/etc/passwd",
		},
		{
			// A scheme-relative path must not become another host: only the
			// path is ever taken from the request.
			name: "scheme-relative path stays on the base host", base: base,
			path: "//evil.example.com/x",
			want: "https://pkg.example.org/FreeBSD:15:amd64/quarterly/evil.example.com/x",
		},
		{
			name: "a dirty base is cleaned too", base: "https://pkg.example.org/a/../b//c/",
			path: "/meta.conf",
			want: "https://pkg.example.org/b/c/meta.conf",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := upstreamURL(tc.base, tc.path, tc.query)
			if err != nil {
				t.Fatalf("upstreamURL(%q, %q) = %v", tc.base, tc.path, err)
			}
			if got != tc.want {
				t.Errorf("upstreamURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
			}
		})
	}
}

func TestUpstreamURLRejectsUnusableBases(t *testing.T) {
	for _, base := range []string{
		"",                        // unset: ADR-006 makes this required
		"pkg+https://pkg/x",       // the form in pkg's own config
		"ftp://pkg.example.org/x", // neither http nor https
		"/no/host",
		"://broken",
	} {
		if got, err := upstreamURL(base, "/meta.conf", ""); err == nil {
			t.Errorf("upstreamURL(%q, …) = %q, want an error", base, got)
		}
	}
}

// --- status codes -----------------------------------------------------------

// The narrowed contract in one table (ADR-003). What changed from the retired
// one: an empty peer list, an unreachable tracker and an exhausted peer list
// are no longer answers to pkg at all -- they go to upstream, and pkg sees the
// 200 that produces. 404 means one thing (absent from the repository database)
// and 502 means one thing (peers AND upstream both failed).
func TestFacadeStatusCodes(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	const pkgPath = "/stable/FreeBSD:15:amd64/latest/All/nginx-1.24.0_2.pkg"
	peerContent := []byte("package bytes from a peer")
	upstreamContent := []byte("package bytes from upstream")

	goodPeer := startPeer(t, fakeCache{pkgName: peerContent})
	// A peer that answers but with the wrong bytes: hash mismatch (UC-02 9c).
	corruptPeer := startPeer(t, fakeCache{pkgName: tamperedLike(peerContent)})
	// A peer that is not listening at all: connection failure (UC-02 8e).
	deadPeer := "127.0.0.1:1"

	// The repository database is the peer path's contract; upstream's bytes
	// are a different file, which is exactly how the tests below tell the two
	// sources apart in the response body.
	repo := fakeRepo{pkgName: {hash: sha256Hex(peerContent), size: int64(len(peerContent))}}

	tests := []struct {
		name         string
		method       string
		path         string
		lister       peer.PeerLister
		repo         Repository
		upstream     http.HandlerFunc
		upstreamURL  string // overrides the stub, for "upstream unreachable"
		wantCode     int
		wantBody     string
		wantUpstream int // requests the upstream must have seen
	}{
		{
			name: "verified download from a peer", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, repo: repo,
			wantCode: http.StatusOK, wantBody: string(peerContent), wantUpstream: 0,
		},
		{
			name: "falls through to a later peer", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{deadPeer, corruptPeer, goodPeer}}, repo: repo,
			wantCode: http.StatusOK, wantBody: string(peerContent), wantUpstream: 0,
		},
		{
			// ADR-005: relayed, not refused. The 404 this used to answer
			// breaks `pkg update` outright (§7.1).
			name: "metadata path", method: http.MethodGet, path: "/stable/FreeBSD:15:amd64/latest/meta.conf",
			lister: fakeLister{addrs: []string{goodPeer}}, repo: repo,
			wantCode: http.StatusOK, wantBody: string(upstreamContent), wantUpstream: 1,
		},
		{
			name: "malformed name-version", method: http.MethodGet, path: "/latest/All/nginx.pkg",
			lister: fakeLister{addrs: []string{goodPeer}}, repo: repo,
			wantCode: http.StatusBadRequest, wantUpstream: 0,
		},
		{
			// The one surviving 404, and upstream is not asked: without a
			// hash and a size there is nothing to bound or verify a transfer
			// with, so the request is provably unanswerable.
			name: "not in the repository database", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, repo: fakeRepo{},
			wantCode: http.StatusNotFound, wantUpstream: 0,
		},
		{
			name: "no repository database wired up", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, repo: nil,
			wantCode: http.StatusNotFound, wantUpstream: 0,
		},
		{
			// The common case, not a fault: nobody in the swarm has it yet.
			// The retired contract answered 404 here, which failed every
			// first-of-its-kind install.
			name: "tracker returned no peers", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: nil}, repo: repo,
			wantCode: http.StatusOK, wantBody: string(upstreamContent), wantUpstream: 1,
		},
		{
			name: "tracker unreachable", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{err: errors.New("dial tcp: connection refused")}, repo: repo,
			wantCode: http.StatusOK, wantBody: string(upstreamContent), wantUpstream: 1,
		},
		{
			name: "all peers exhausted", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: []string{deadPeer, corruptPeer}}, repo: repo,
			wantCode: http.StatusOK, wantBody: string(upstreamContent), wantUpstream: 1,
		},
		{
			// The only 502 there is: both sources gone.
			name: "peers and upstream both failed", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: nil}, repo: repo,
			upstream: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "mirror is having a bad day", http.StatusInternalServerError)
			},
			wantCode: http.StatusBadGateway, wantUpstream: 1,
		},
		{
			// ADR-007: the repository database knows every catalogue on the
			// host, but upstream_url fronts one repository. A 404 from
			// upstream for a package we hold a hash for is that gap, and it
			// is reported rather than assumed impossible.
			name: "upstream does not hold it either", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: nil}, repo: repo,
			upstream: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "no such package", http.StatusNotFound)
			},
			wantCode: http.StatusBadGateway, wantUpstream: 1,
		},
		{
			name: "upstream unreachable", method: http.MethodGet, path: pkgPath,
			lister: fakeLister{addrs: nil}, repo: repo,
			upstreamURL: "http://127.0.0.1:1",
			wantCode:    http.StatusBadGateway,
		},
		{
			name: "HEAD is not served", method: http.MethodHead, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, repo: repo,
			wantCode: http.StatusMethodNotAllowed, wantUpstream: 0,
		},
		{
			name: "POST is not served", method: http.MethodPost, path: pkgPath,
			lister: fakeLister{addrs: []string{goodPeer}}, repo: repo,
			wantCode: http.StatusMethodNotAllowed, wantUpstream: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.upstream
			if handler == nil {
				handler = func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write(upstreamContent)
				}
			}
			up := startUpstream(t, handler)

			url := up.URL
			if tc.upstreamURL != "" {
				url = tc.upstreamURL
			}
			f := &Facade{Peers: tc.lister, Repo: tc.repo, UpstreamURL: url}

			rec := httptest.NewRecorder()
			f.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.upstreamURL == "" {
				if got := len(up.requests()); got != tc.wantUpstream {
					t.Errorf("upstream saw %d request(s), want %d: %+v", got, tc.wantUpstream, up.requests())
				}
			}
			if rec.Code != http.StatusOK {
				return
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("Content-Type = %q, want application/octet-stream", ct)
			}
			if body, _ := io.ReadAll(rec.Body); string(body) != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

// --- the metadata branch (ADR-005, UC-07) -----------------------------------

// The catalogue is what `pkg update` fetches, and it is the one thing the
// facade cannot answer from itself: the repository database carries no hash
// for it, and is itself the thing being updated. So it is relayed -- and
// relaying is not vouching, because pkg checks the repository signature
// against its own fingerprints.
func TestFacadeRelaysMetadataFromUpstream(t *testing.T) {
	const catalogue = "signed catalogue bytes"
	const lastModified = "Thu, 06 Aug 2026 18:39:36 GMT"

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-xz")
		w.Header().Set("Last-Modified", lastModified)
		_, _ = io.WriteString(w, catalogue)
	})
	f := &Facade{
		Peers:       fakeLister{addrs: []string{"127.0.0.1:1"}},
		Repo:        fakeRepo{},
		UpstreamURL: up.URL + "/FreeBSD:15:amd64/quarterly",
	}

	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/packagesite.pkg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != catalogue {
		t.Errorf("body = %q, want the upstream bytes %q", got, catalogue)
	}
	// Relayed unmodified: pkg needs the content type, and needs
	// Last-Modified so its NEXT request can be conditional.
	if got := rec.Header().Get("Content-Type"); got != "application/x-xz" {
		t.Errorf("Content-Type = %q, want it relayed unchanged", got)
	}
	if got := rec.Header().Get("Last-Modified"); got != lastModified {
		t.Errorf("Last-Modified = %q, want %q", got, lastModified)
	}

	got := up.requests()
	if len(got) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(got))
	}
	if want := "/FreeBSD:15:amd64/quarterly/packagesite.pkg"; got[0].Path != want {
		t.Errorf("upstream path = %q, want %q", got[0].Path, want)
	}
}

// pkg sends conditional GETs for catalogue files (measured, §7.3). ADR-005
// requires the header to be forwarded and the 304 to be RELAYED, never
// synthesised: the daemon tracks no upstream modification times, and a guess
// serves a stale catalogue. Without this, every `pkg update` re-downloads a
// catalogue the swarm cannot help with.
func TestFacadeRelaysConditionalGetAnd304(t *testing.T) {
	const ims = "Thu, 06 Aug 2026 18:39:36 GMT"

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Modified-Since") == ims {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, "a whole catalogue nobody needed")
	})
	f := &Facade{Peers: fakeLister{}, Repo: fakeRepo{}, UpstreamURL: up.URL}

	req := httptest.NewRequest(http.MethodGet, "/meta.conf", nil)
	req.Header.Set("If-Modified-Since", ims)
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("304 carried a %d-byte body (%q); RFC 9110 §15.4.5 forbids one", len(body), body)
	}
	if got := up.requests(); len(got) != 1 || got[0].IMS != ims {
		t.Errorf("upstream saw %+v, want one request carrying If-Modified-Since", got)
	}
}

// Upstream's status is upstream's answer. The facade has no standing to
// reinterpret a 404 on a catalogue path as anything else, and turning it into
// a 502 would tell pkg the mirror is broken when it is merely saying no.
func TestFacadeRelaysUpstreamStatusForMetadata(t *testing.T) {
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such file", http.StatusNotFound)
	})
	f := &Facade{Peers: fakeLister{}, Repo: fakeRepo{}, UpstreamURL: up.URL}

	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no-such-catalogue", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want upstream's own 404 relayed", rec.Code)
	}
}

// The facade's own code, and the only one it may send on this branch: the
// fetch itself failed. Terminal -- there is no next mirror, so `pkg update`
// fails outright (UC-07 error state 1).
func TestFacadeMetadataRelayFailureIs502(t *testing.T) {
	f := &Facade{Peers: fakeLister{}, Repo: fakeRepo{}, UpstreamURL: "http://127.0.0.1:1"}

	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/meta.conf", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

// No spool and no cache on this branch either: the catalogue is large, the
// reference host has 1 GiB, and there is nothing to withhold bytes for.
func TestFacadeRelayDoesNotSpoolOrCache(t *testing.T) {
	tempDir := t.TempDir()
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "catalogue")
	})
	f := &Facade{Peers: fakeLister{}, Repo: fakeRepo{}, TempDir: tempDir, UpstreamURL: up.URL}

	for i := range 2 {
		rec := httptest.NewRecorder()
		f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/packagesite.pkg", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, rec.Code)
		}
	}
	if got := len(up.requests()); got != 2 {
		t.Errorf("upstream saw %d requests, want 2 -- the facade must hold no cache", got)
	}
	assertEmptyDir(t, tempDir)
}

// Hop-by-hop headers describe one connection, not the resource, and must not
// cross the relay (RFC 9110 §7.6.1) -- including the ones upstream names in its
// own Connection header.
func TestFacadeRelayDropsHopByHopHeaders(t *testing.T) {
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "X-Mirror-Private")
		w.Header().Set("X-Mirror-Private", "internal")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("X-Mirror-Public", "kept")
		_, _ = io.WriteString(w, "catalogue")
	})
	f := &Facade{Peers: fakeLister{}, Repo: fakeRepo{}, UpstreamURL: up.URL}

	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/meta.conf", nil))

	for _, h := range []string{"Connection", "Keep-Alive", "X-Mirror-Private"} {
		if got := rec.Header().Get(h); got != "" {
			t.Errorf("%s = %q, want it dropped at the relay", h, got)
		}
	}
	if got := rec.Header().Get("X-Mirror-Public"); got != "kept" {
		t.Errorf("X-Mirror-Public = %q, want it relayed", got)
	}
}

// The upstream request must be the path pkg asked for, joined onto the
// configured base -- including the Hashed/~hash10 form, which is what a real
// mirror is addressed with and what the daemon must not rewrite.
func TestFacadeForwardsThePackagePathUpstream(t *testing.T) {
	const nv = "indexinfo-0.3.1_1"
	const reqPath = "/stable/FreeBSD:15:amd64/latest/All/Hashed/indexinfo-0.3.1_1~ae9dce33aa.pkg"
	content := []byte("indexinfo bytes")

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	})
	f := &Facade{
		Peers:       fakeLister{},
		Repo:        fakeRepo{nv: {hash: sha256Hex(content), size: int64(len(content))}},
		UpstreamURL: up.URL + "/mirror-root",
	}

	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, reqPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	got := up.requests()
	if len(got) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(got))
	}
	if want := "/mirror-root" + reqPath; got[0].Path != want {
		t.Errorf("upstream path = %q, want %q", got[0].Path, want)
	}
	if got[0].Method != http.MethodGet {
		t.Errorf("upstream method = %q, want GET", got[0].Method)
	}
}

// The Content-Length promised to pkg is packages.pkgsize from the same
// repository-database row as the hash (UC-02 §9f), not a count of upstream's
// bytes: the facade commits to a correct length rather than to whatever the
// mirror produces.
func TestFacadeUpstreamContentLengthComesFromTheRepositoryDatabase(t *testing.T) {
	const nv = "curl-8.6.0"
	content := []byte("curl bytes")

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	})
	f := &Facade{
		Peers:       fakeLister{},
		Repo:        fakeRepo{nv: {hash: sha256Hex(content), size: int64(len(content))}},
		UpstreamURL: up.URL,
	}

	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/latest/All/curl-8.6.0.pkg")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.ContentLength != int64(len(content)) {
		t.Errorf("Content-Length = %d, want %d", resp.ContentLength, len(content))
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != string(content) {
		t.Errorf("body = %q, want %q", body, content)
	}
}

// A corrupt peer must never have its bytes reach pkg, even when it is the only
// peer available -- the whole point of end-to-end verification. What changed
// under ADR-003 is what happens next: the request is not failed, it is served
// from upstream.
func TestFacadeNeverServesUnverifiedBytes(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	content := []byte("package bytes")
	corruptPeer := startPeer(t, fakeCache{pkgName: tamperedLike(content)})

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	})
	f := &Facade{
		Peers:       fakeLister{addrs: []string{corruptPeer}},
		Repo:        fakeRepo{pkgName: {hash: sha256Hex(content), size: int64(len(content))}},
		UpstreamURL: up.URL,
	}
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/latest/All/nginx-1.24.0_2.pkg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 from upstream", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) == string(tamperedLike(content)) {
		t.Fatal("facade served unverified bytes to pkg")
	}
	if string(body) != string(content) {
		t.Errorf("body = %q, want the upstream copy %q", body, content)
	}
	if !f.Blacklist.Blocked(corruptPeer) {
		t.Error("corrupt peer was not blacklisted")
	}
}

// UC-02 §11c/§7: the blacklist is the daemon's, not the request's. A peer that
// serves corrupt bytes for one request must be skipped by the next one.
func TestFacadeBlacklistOutlivesOneRequest(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	const pkgPath = "/latest/All/nginx-1.24.0_2.pkg"
	content := []byte("package bytes")

	corruptPeer := startPeer(t, fakeCache{pkgName: tamperedLike(content)})
	goodPeer := startPeer(t, fakeCache{pkgName: content})

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be reached: a peer can serve this")
	})
	f := &Facade{
		Peers:       fakeLister{addrs: []string{corruptPeer, goodPeer}},
		Repo:        fakeRepo{pkgName: {hash: sha256Hex(content), size: int64(len(content))}},
		UpstreamURL: up.URL,
	}

	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pkgPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec.Code)
	}
	if !f.Blacklist.Blocked(corruptPeer) {
		t.Fatal("corrupt peer was not blacklisted")
	}

	// Second request: same peer list, and the corrupt peer must not be tried
	// again. (peer-level tests assert the skip by counting connections.)
	rec = httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pkgPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want 200", rec.Code)
	}
	if f.Blacklist.Blocked(goodPeer) {
		t.Fatal("the verifying peer must not be blacklisted")
	}
	if got := f.Blacklist.Addrs(); len(got) != 1 || got[0] != corruptPeer {
		t.Fatalf("blacklist = %v, want exactly [%s]", got, corruptPeer)
	}
}

// Everything above drives the handler directly. pkg is a real HTTP client on
// a real socket, so at least one path is worth exercising over the wire: it is
// the only way the status line, headers and body are actually serialised.
func TestFacadeOverRealHTTP(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	content := []byte("package bytes over the wire")

	const catalogue = "signed catalogue bytes over the wire"
	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, catalogue)
	})
	f := &Facade{
		Peers:       fakeLister{addrs: []string{startPeer(t, fakeCache{pkgName: content})}},
		Repo:        fakeRepo{pkgName: {hash: sha256Hex(content), size: int64(len(content))}},
		UpstreamURL: up.URL,
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	// The worked mirror URL from the spec, against the daemon.
	resp, err := http.Get(srv.URL + "/stable/FreeBSD:15:amd64/latest/All/nginx-1.24.0_2.pkg")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.ContentLength; got != int64(len(content)) {
		t.Errorf("Content-Length = %d, want %d", got, len(content))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != string(content) {
		t.Errorf("body = %q, want %q", body, content)
	}

	// UC-07 over the same wire: metadata is relayed, not refused.
	metaResp, err := http.Get(srv.URL + "/stable/FreeBSD:15:amd64/latest/meta.conf")
	if err != nil {
		t.Fatalf("GET meta.conf: %v", err)
	}
	defer metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK {
		t.Errorf("meta.conf status = %d, want 200 relayed from upstream", metaResp.StatusCode)
	}
	metaBody, _ := io.ReadAll(metaResp.Body)
	if string(metaBody) != catalogue {
		t.Errorf("meta.conf body = %q, want the upstream catalogue %q", metaBody, catalogue)
	}
}

// UC-02 §8: a peer download is spooled through temp_dir, and the buffer is
// per-request -- nothing may be left behind for the next one to find.
func TestFacadePeerPathSpoolsThroughTempDirAndCleansUp(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	content := []byte("package bytes")
	tempDir := t.TempDir()

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be reached: a peer can serve this")
	})
	f := &Facade{
		Peers:       fakeLister{addrs: []string{startPeer(t, fakeCache{pkgName: content})}},
		Repo:        fakeRepo{pkgName: {hash: sha256Hex(content), size: int64(len(content))}},
		TempDir:     tempDir,
		UpstreamURL: up.URL,
	}
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/latest/All/nginx-1.24.0_2.pkg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != string(content) {
		t.Errorf("body = %q, want %q", got, content)
	}
	assertEmptyDir(t, tempDir)
}

// ADR-003's asymmetry, as a test: the upstream path streams and must not spool.
// The cost it avoids is temp space sized to the largest package in the
// repository (2.83 GiB) on every peer miss.
func TestFacadeUpstreamPathDoesNotTouchTempDir(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	content := []byte("package bytes")
	tempDir := t.TempDir()

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	})
	f := &Facade{
		Peers:       fakeLister{}, // no peers: straight to upstream
		Repo:        fakeRepo{pkgName: {hash: sha256Hex(content), size: int64(len(content))}},
		TempDir:     tempDir,
		UpstreamURL: up.URL,
	}
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/latest/All/nginx-1.24.0_2.pkg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	assertEmptyDir(t, tempDir)
}

// A temp_dir the daemon cannot write is a peer-path failure like any other. It
// used to be a 500, which under ADR-003 is a code the facade is no longer
// entitled to send: the upstream path does not touch temp_dir and can still
// serve the request, and an error reaches pkg only when both sources are gone.
func TestFacadeUnwritableTempDirGoesToUpstream(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	content := []byte("package bytes")

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	})
	f := &Facade{
		Peers:       fakeLister{addrs: []string{startPeer(t, fakeCache{pkgName: content})}},
		Repo:        fakeRepo{pkgName: {hash: sha256Hex(content), size: int64(len(content))}},
		TempDir:     filepath.Join(t.TempDir(), "does-not-exist"),
		UpstreamURL: up.URL,
	}
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/latest/All/nginx-1.24.0_2.pkg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 from upstream", rec.Code)
	}
	if len(up.requests()) != 1 {
		t.Errorf("upstream saw %d requests, want 1", len(up.requests()))
	}
}

// Three documents forbid a daemon-owned store, so a second request for the same
// package must go to upstream again rather than being answered from anything
// the first one kept.
func TestFacadeDoesNotCacheUpstreamBytes(t *testing.T) {
	const pkgName = "nginx-1.24.0_2"
	content := []byte("package bytes")
	tempDir := t.TempDir()

	up := startUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	})
	f := &Facade{
		Peers:       fakeLister{},
		Repo:        fakeRepo{pkgName: {hash: sha256Hex(content), size: int64(len(content))}},
		TempDir:     tempDir,
		UpstreamURL: up.URL,
	}

	for i := range 2 {
		rec := httptest.NewRecorder()
		f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/latest/All/nginx-1.24.0_2.pkg", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, rec.Code)
		}
	}
	if got := len(up.requests()); got != 2 {
		t.Errorf("upstream saw %d requests, want 2 -- the facade must hold no cache", got)
	}
	assertEmptyDir(t, tempDir)
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("temp_dir still holds %v; the buffer must not outlive the request", names)
	}
}

func TestFacadeCheck(t *testing.T) {
	const upstream = "https://pkg.example.org/FreeBSD:15:amd64/quarterly"

	if err := (&Facade{}).Check(); err == nil {
		t.Error("Check() on an empty facade = nil, want an error")
	}
	f := &Facade{Peers: fakeLister{}, UpstreamURL: upstream}
	if err := f.Check(); !errors.Is(err, ErrNoRepositoryDatabase) {
		t.Errorf("Check() without hashes = %v, want ErrNoRepositoryDatabase", err)
	}
	f.Repo = fakeRepo{}

	// ADR-006: upstream_url is required and has no default. A facade without
	// one cannot answer a peer miss, so it must not be allowed to listen.
	f.UpstreamURL = ""
	if err := f.Check(); err == nil {
		t.Error("Check() without an upstream = nil, want an error")
	}

	f.UpstreamURL = upstream
	if err := f.Check(); err != nil {
		t.Errorf("Check() on a wired facade = %v, want nil", err)
	}
}
