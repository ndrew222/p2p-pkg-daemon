package peer

// The seeding side: the response table from docs/peer-transfer-spec-v0.2.md,
// the UC-06 §5b re-announce obligation, and ADR-002's two caps.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The spec's Responses table, end to end over a real socket.
func TestSeederResponses(t *testing.T) {
	cache := t.TempDir()
	content := []byte("real end to end package")
	writePackage(t, cache, "jq-1.7", content)

	addr := startSeeder(t, &Server{Source: dirSource(cache)})
	base := "http://" + addr

	tests := []struct {
		name     string
		method   string
		url      string
		wantCode int
		wantBody string
	}{
		{"held", http.MethodGet, base + "/pkg/jq-1.7", http.StatusOK, string(content)},

		// Not held: the ordinary case after a pkg clean.
		{"not held", http.MethodGet, base + "/pkg/notheld-1.0", http.StatusNotFound, ""},

		// Everything that is not exactly /pkg/<one segment>. 400 rather
		// than 404, because 404 carries the re-announce obligation and a
		// malformed path is no evidence about what we hold.
		{"root", http.MethodGet, base + "/", http.StatusBadRequest, ""},
		{"prefix only", http.MethodGet, base + "/pkg/", http.StatusBadRequest, ""},
		{"deeper tree", http.MethodGet, base + "/pkg/jq-1.7/extra", http.StatusBadRequest, ""},
		{"facade namespace", http.MethodGet, base + "/All/jq-1.7.pkg", http.StatusBadRequest, ""},
		{"hashed facade namespace", http.MethodGet, base + "/latest/All/Hashed/jq-1.7~0123456789.pkg", http.StatusBadRequest, ""},

		// Metadata has no representation on this wire at all. A seeding
		// daemon is not a pkg mirror and must not look like one.
		{"packagesite", http.MethodGet, base + "/packagesite.pkg", http.StatusBadRequest, ""},
		{"meta.conf", http.MethodGet, base + "/meta.conf", http.StatusBadRequest, ""},

		// Method other than GET (HEAD excepted, below).
		{"POST", http.MethodPost, base + "/pkg/jq-1.7", http.StatusMethodNotAllowed, ""},
		{"PUT", http.MethodPut, base + "/pkg/jq-1.7", http.StatusMethodNotAllowed, ""},
		{"DELETE", http.MethodDelete, base + "/pkg/jq-1.7", http.StatusMethodNotAllowed, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.method, tc.url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantCode)
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
				t.Errorf("Content-Type = %q, want application/octet-stream", ct)
			}
			if resp.ContentLength != int64(len(tc.wantBody)) {
				t.Errorf("Content-Length = %d, want %d", resp.ContentLength, len(tc.wantBody))
			}
			body, _ := io.ReadAll(resp.Body)
			if string(body) != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

// HEAD is answered because http.ServeContent gives it to us for free. Nothing
// depends on it and the requester never sends one; this pins that it does not
// accidentally become an error.
func TestSeederAnswersHEADForFree(t *testing.T) {
	cache := t.TempDir()
	content := []byte("real end to end package")
	writePackage(t, cache, "jq-1.7", content)
	addr := startSeeder(t, &Server{Source: dirSource(cache)})

	resp, err := http.Head("http://" + addr + "/pkg/jq-1.7")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.ContentLength != int64(len(content)) {
		t.Errorf("Content-Length = %d, want %d", resp.ContentLength, len(content))
	}
}

// UC-06 §5b: a serving daemon that discovers it does not hold a requested
// package sends a FULL re-announce, because if one entry has drifted others
// may have too.
//
// It attaches to 404 and to nothing else. A 503 must never trigger it: 404
// means we have discovered we no longer hold something we advertised, whereas
// 503 means we do hold it and are refusing right now, and re-announcing then
// would flood the tracker precisely when the daemon is already at its limit.
func TestReAnnounceOn404Only(t *testing.T) {
	tests := []struct {
		name       string
		srv        func(chan string) *Server
		path       string
		wantCode   int
		wantNotify bool
	}{
		{
			name: "404 re-announces",
			srv: func(ch chan string) *Server {
				return &Server{Source: dirSource(t.TempDir()), OnNotHeld: func(nv string) { ch <- nv }}
			},
			path: "/pkg/gone-1.0", wantCode: http.StatusNotFound, wantNotify: true,
		},
		{
			name: "400 does not re-announce",
			srv: func(ch chan string) *Server {
				return &Server{Source: dirSource(t.TempDir()), OnNotHeld: func(nv string) { ch <- nv }}
			},
			path: "/not-the-peer-namespace", wantCode: http.StatusBadRequest, wantNotify: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan string, 1)
			addr := startSeeder(t, tc.srv(ch))

			resp, err := http.Get("http://" + addr + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantCode)
			}

			select {
			case nv := <-ch:
				if !tc.wantNotify {
					t.Errorf("a %d triggered a re-announce for %q", tc.wantCode, nv)
				}
			case <-time.After(500 * time.Millisecond):
				if tc.wantNotify {
					t.Error("a 404 did not trigger the UC-06 §5b re-announce")
				}
			}
		})
	}
}

// The 503 half of the rule above, plus two properties of the refusal itself.
//
// A 503 must not re-announce: it means the opposite of a 404. We hold the
// package and are refusing to serve it right now, so re-announcing would flood
// the tracker precisely when the daemon is already at its limit.
//
// And it must not carry Retry-After. The requester has other holders to try
// and pkg's own mirror behind those, so inviting it to wait converts a fast
// fall-through into a stall -- the exact failure mode ADR-002 rejected a
// listener-level limit for.
func TestRefusalDoesNotReAnnounceAndDoesNotInviteAWait(t *testing.T) {
	announced := make(chan string, 1)
	src := newBlockingSource(1 << 20)
	srv := &Server{
		Source:             src,
		MaxConcurrentSeeds: 1,
		OnNotHeld:          func(nv string) { announced <- nv },
	}
	addr := startSeeder(t, srv)

	// Hold the only slot.
	held := make(chan struct{})
	go func() {
		defer close(held)
		resp, err := http.Get("http://" + addr + "/pkg/jq-1.7")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	src.waitInFlight(t, 1)

	resp, err := http.Get("http://" + addr + "/pkg/jq-1.7")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want it absent", got)
	}

	select {
	case nv := <-announced:
		t.Errorf("a 503 triggered a re-announce for %q", nv)
	case <-time.After(300 * time.Millisecond):
	}

	src.releaseAll()
	<-held
}

// Every handle the seeder opens must be closed. A leaked descriptor per
// request would exhaust the very resource ADR-002's caps exist to protect --
// one shared with the facade's outbound fetches and the tracker keep-alive --
// and would do it whatever the caps were set to.
func TestSeederClosesTheSourceHandle(t *testing.T) {
	src := newBlockingSource(16)
	src.releaseAll() // nothing parks; the reader only records its Close
	addr := startSeeder(t, &Server{Source: src})

	resp, err := http.Get("http://" + addr + "/pkg/jq-1.7")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	r := src.nth(1)
	if r == nil {
		t.Fatal("the source was never opened")
	}
	if !r.isClosed() {
		t.Error("the seeder did not close the handle it opened")
	}
}

// ADR-002's two caps, driven through the handler directly so the remote
// address is under the test's control. Over a loopback socket every request
// comes from 127.0.0.1, which cannot distinguish the per-IP cap from the
// global one.
func TestSeedingCaps(t *testing.T) {
	tests := []struct {
		name        string
		maxGlobal   int
		maxPerIP    int
		holders     []string // remote addresses that take and keep a slot
		probe       string   // the address whose request is then measured
		wantCode    int
		wantLogHint string
	}{
		{
			name:    "unlimited by default",
			holders: []string{"10.0.0.1:1", "10.0.0.1:2", "10.0.0.1:3", "10.0.0.2:1"},
			probe:   "10.0.0.1:4", wantCode: http.StatusOK,
		},
		{
			name:      "the global cap refuses everyone",
			maxGlobal: 2,
			holders:   []string{"10.0.0.1:1", "10.0.0.2:1"},
			probe:     "10.0.0.3:1", wantCode: http.StatusServiceUnavailable,
		},
		{
			name:      "under the global cap is served",
			maxGlobal: 3,
			holders:   []string{"10.0.0.1:1", "10.0.0.2:1"},
			probe:     "10.0.0.3:1", wantCode: http.StatusOK,
		},
		{
			name:     "the per-IP cap refuses one source",
			maxPerIP: 2,
			holders:  []string{"10.0.0.1:1", "10.0.0.1:2"},
			probe:    "10.0.0.1:3", wantCode: http.StatusServiceUnavailable,
		},
		{
			// The point of having both. A source at its own
			// ceiling must not deny anyone else -- otherwise one
			// hostile IP takes the whole seeding surface, which is
			// what the global cap alone could not prevent.
			name:     "the per-IP cap does not refuse a different source",
			maxPerIP: 2,
			holders:  []string{"10.0.0.1:1", "10.0.0.1:2"},
			probe:    "10.0.0.9:1", wantCode: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := newBlockingSource(1 << 20)
			srv := &Server{
				Source:                  src,
				MaxConcurrentSeeds:      tc.maxGlobal,
				MaxConcurrentSeedsPerIP: tc.maxPerIP,
			}

			var wg sync.WaitGroup
			for _, addr := range tc.holders {
				wg.Add(1)
				go func() {
					defer wg.Done()
					req := httptest.NewRequest(http.MethodGet, "/pkg/jq-1.7", nil)
					req.RemoteAddr = addr
					srv.ServeHTTP(httptest.NewRecorder(), req)
				}()
			}
			src.waitInFlight(t, len(tc.holders))

			req := httptest.NewRequest(http.MethodGet, "/pkg/jq-1.7", nil)
			req.RemoteAddr = tc.probe
			rec := httptest.NewRecorder()

			done := make(chan struct{})
			go func() { srv.ServeHTTP(rec, req); close(done) }()
			if tc.wantCode == http.StatusOK {
				// A served probe parks in Read like the rest.
				src.waitInFlight(t, len(tc.holders)+1)
			} else {
				<-done
			}

			src.releaseAll()
			<-done
			wg.Wait()

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

// A released slot is returned. Without this the caps would be a one-way
// ratchet and the seeder would stop serving after N requests forever.
func TestSeedingSlotsAreReleased(t *testing.T) {
	cache := t.TempDir()
	writePackage(t, cache, "jq-1.7", []byte("bytes"))
	addr := startSeeder(t, &Server{
		Source:                  dirSource(cache),
		MaxConcurrentSeeds:      1,
		MaxConcurrentSeedsPerIP: 1,
	})

	for i := 0; i < 5; i++ {
		resp, err := http.Get("http://" + addr + "/pkg/jq-1.7")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200; the slot from the previous request was not released", i, resp.StatusCode)
		}
	}
}

// Remote identity is the connection's source address and NEVER a header. A cap
// keyed on client-supplied input is a cap the attacker sets: one forged
// X-Forwarded-For per request and the per-IP cap becomes unlimited.
func TestPerIPCapIgnoresForwardingHeaders(t *testing.T) {
	src := newBlockingSource(1 << 20)
	srv := &Server{Source: src, MaxConcurrentSeedsPerIP: 1}

	held := httptest.NewRequest(http.MethodGet, "/pkg/jq-1.7", nil)
	held.RemoteAddr = "10.0.0.1:1"
	done := make(chan struct{})
	go func() { srv.ServeHTTP(httptest.NewRecorder(), held); close(done) }()
	src.waitInFlight(t, 1)

	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"} {
		req := httptest.NewRequest(http.MethodGet, "/pkg/jq-1.7", nil)
		req.RemoteAddr = "10.0.0.1:2" // the same host, a different port
		req.Header.Set(header, "203.0.113.99")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503; the cap must key on RemoteAddr, never a header", header, rec.Code)
		}
	}

	src.releaseAll()
	<-done
}

// The diagnostic has to name the cap and the IP: an attack and a misconfigured
// ceiling look identical in a bare count and have opposite remedies (ADR-002).
func TestRefusalNamesTheCapAndTheIP(t *testing.T) {
	tests := []struct {
		name      string
		maxGlobal int
		maxPerIP  int
		wantIn    []string
	}{
		{"global", 1, 0, []string{"max_concurrent_seeds", "10.0.0.7"}},
		{"per ip", 0, 1, []string{"max_concurrent_seeds_per_ip", "10.0.0.7"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newSeedLimiter(tc.maxGlobal, tc.maxPerIP)
			release, _, ok := l.acquire("10.0.0.7")
			if !ok {
				t.Fatal("the first acquire was refused")
			}
			defer release()

			_, refused, ok := l.acquire("10.0.0.7")
			if ok {
				t.Fatal("the second acquire was allowed past the cap")
			}
			line := refused.reason + " " + refused.ip + " " + refused.inFlight
			for _, want := range tc.wantIn {
				if !strings.Contains(line, want) {
					t.Errorf("diagnostic %q does not mention %q", line, want)
				}
			}
		})
	}
}

// The per-IP table must not grow without bound as source addresses churn --
// otherwise a cap meant to protect memory becomes a way of consuming it.
func TestPerIPTableDoesNotGrowWithChurn(t *testing.T) {
	l := newSeedLimiter(0, 4)
	for i := 0; i < 1000; i++ {
		release, _, ok := l.acquire("10.0.0.1")
		if !ok {
			t.Fatalf("acquire %d refused under an unlimited global cap", i)
		}
		release()
		release() // idempotent: a handler may defer it and also call it
	}
	l.mu.Lock()
	n := len(l.perIP)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("per-IP table holds %d entries after every slot was released, want 0", n)
	}
}

// A nil Source is a half-wired seeder, which must degrade to "holds nothing"
// rather than taking the daemon down with a nil dereference on the first
// hostile request.
func TestNilSourceHoldsNothing(t *testing.T) {
	addr := startSeeder(t, &Server{})
	resp, err := http.Get("http://" + addr + "/pkg/jq-1.7")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// Close must end Serve. The hand-written accept loop this replaced logged and
// continued on every Accept error, so closing the listener span it at full
// tilt instead of stopping it.
func TestServeReturnsAfterClose(t *testing.T) {
	srv := &Server{Source: dirSource(t.TempDir())}
	ln := mustListen(t)

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	// Give Serve a moment to install its http.Server before closing.
	for i := 0; i < 100; i++ {
		if resp, err := http.Get("http://" + ln.Addr().String() + "/pkg/x-1"); err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			t.Fatalf("Serve returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
}
