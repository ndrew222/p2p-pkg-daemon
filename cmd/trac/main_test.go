package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
	"github.com/ndrew222/p2p-pkg-daemon/internal/tracker"
)

// do drives the real mux, with a caller-chosen source address. The source
// address is the peer's identity under v0.2, so the tests have to control it;
// httptest.NewServer would make every client 127.0.0.1.
func do(t *testing.T, h http.Handler, method, target, body, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func announceBody(port int, packages ...string) string {
	b, err := json.Marshal(proto.AnnounceRequest{ServingPort: port, Packages: packages})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func decodePeers(t *testing.T, rec *httptest.ResponseRecorder) []proto.PeerInfo {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp proto.PeerListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode peer list %q: %v", rec.Body.String(), err)
	}
	return resp.Peers
}

// v0.2 §One complete life cycle, over the HTTP surface, in order.
func TestLifeCycleOverHTTP(t *testing.T) {
	h := newMux(tracker.New())
	const addr = "203.0.113.7:51000"

	// daemon boots -> ping -> unknown IP -> 404 {"status":"unknown"}
	rec := do(t, h, http.MethodPost, "/ping", "", addr)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("first ping = %d, want 404", rec.Code)
	}
	var status proto.StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode ping 404 body: %v", err)
	}
	if status.Status != proto.StatusUnknown {
		t.Errorf("ping 404 body = %q, want %q", status.Status, proto.StatusUnknown)
	}

	// -> announce -> 200, registered
	rec = do(t, h, http.MethodPost, "/announce", announceBody(4711, "nginx-1.24.0_2", "curl-8.6.0"), addr)
	if rec.Code != http.StatusOK {
		t.Fatalf("announce = %d, want 200", rec.Code)
	}

	// The IP comes from the connection, and the port from the body.
	peers := decodePeers(t, do(t, h, http.MethodGet, "/peers?pkg=nginx-1.24.0_2", "", "198.51.100.9:40000"))
	if len(peers) != 1 {
		t.Fatalf("peers = %v, want one entry", peers)
	}
	if peers[0].IP != "203.0.113.7" || peers[0].Port != 4711 {
		t.Errorf("peer = %+v, want {203.0.113.7 4711}", peers[0])
	}

	// every 20s -> ping -> 200
	if rec := do(t, h, http.MethodPost, "/ping", "", addr); rec.Code != http.StatusOK {
		t.Errorf("keep-alive ping = %d, want 200", rec.Code)
	}

	// daemon gets a new package -> unprompted announce with the full list
	do(t, h, http.MethodPost, "/announce", announceBody(4711, "nginx-1.24.0_2", "curl-8.6.0", "jq-1.7"), addr)
	if peers := decodePeers(t, do(t, h, http.MethodGet, "/peers?pkg=jq-1.7", "", addr)); len(peers) != 1 {
		t.Errorf("jq-1.7 peers = %v, want one entry", peers)
	}

	// pkg clean -> empty announce -> 200, entry deleted
	rec = do(t, h, http.MethodPost, "/announce", announceBody(4711), addr)
	if rec.Code != http.StatusOK {
		t.Errorf("empty announce = %d, want 200", rec.Code)
	}
	if peers := decodePeers(t, do(t, h, http.MethodGet, "/peers?pkg=nginx-1.24.0_2", "", addr)); len(peers) != 0 {
		t.Errorf("after deregistration, peers = %v, want none", peers)
	}
	// and the daemon is now unknown again
	if rec := do(t, h, http.MethodPost, "/ping", "", addr); rec.Code != http.StatusNotFound {
		t.Errorf("ping after deregistration = %d, want 404", rec.Code)
	}
}

// An empty announce from an IP the tracker never knew is still a plain 200.
func TestEmptyAnnounceWithNothingToDelete(t *testing.T) {
	h := newMux(tracker.New())
	rec := do(t, h, http.MethodPost, "/announce", announceBody(4711), "203.0.113.7:51000")
	if rec.Code != http.StatusOK {
		t.Errorf("empty announce from an unknown ip = %d, want 200", rec.Code)
	}
}

// A ping carries no body, but one that arrives anyway is ignored rather than
// rejected -- "No body (any body is ignored)".
func TestPingIgnoresBody(t *testing.T) {
	h := newMux(tracker.New())
	const addr = "203.0.113.7:51000"

	do(t, h, http.MethodPost, "/announce", announceBody(4711, "nginx-1.24.0_2"), addr)

	for _, body := range []string{"", "{}", `{"peer_id":"legacy"}`, "not json at all"} {
		if rec := do(t, h, http.MethodPost, "/ping", body, addr); rec.Code != http.StatusOK {
			t.Errorf("ping with body %q = %d, want 200", body, rec.Code)
		}
	}
}

// The IP is the connection's source address and nothing else. A daemon must
// not be able to register on another machine's behalf.
func TestIdentityIgnoresForwardedHeaders(t *testing.T) {
	h := newMux(tracker.New())

	req := httptest.NewRequest(http.MethodPost, "/announce",
		strings.NewReader(announceBody(4711, "nginx-1.24.0_2")))
	req.RemoteAddr = "203.0.113.7:51000"
	req.Header.Set("X-Forwarded-For", "198.51.100.4")
	req.Header.Set("X-Real-IP", "198.51.100.4")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	peers := decodePeers(t, do(t, h, http.MethodGet, "/peers?pkg=nginx-1.24.0_2", "", "203.0.113.7:51000"))
	if len(peers) != 1 {
		t.Fatalf("peers = %v, want one entry", peers)
	}
	if peers[0].IP != "203.0.113.7" {
		t.Errorf("registered ip = %q, want the connection's source address 203.0.113.7", peers[0].IP)
	}
}

func TestPeersMissIsEmptyArray(t *testing.T) {
	h := newMux(tracker.New())
	rec := do(t, h, http.MethodGet, "/peers?pkg=nothing-1.0", "", "203.0.113.7:51000")

	if rec.Code != http.StatusOK {
		t.Errorf("miss = %d, want 200 (the only 404 is the unknown pinger)", rec.Code)
	}
	// Literal check: a nil slice would marshal to `null` and break clients
	// that expect to iterate the field.
	if got := strings.TrimSpace(rec.Body.String()); got != `{"peers":[]}` {
		t.Errorf("miss body = %s, want {\"peers\":[]}", got)
	}
}

func TestPeersCapsAtMaxPeers(t *testing.T) {
	h := newMux(tracker.New())
	for i := range 10 {
		addr := fmt.Sprintf("203.0.113.%d:51000", i+1)
		do(t, h, http.MethodPost, "/announce", announceBody(4711+i, "nginx-1.24.0_2"), addr)
	}

	peers := decodePeers(t, do(t, h, http.MethodGet, "/peers?pkg=nginx-1.24.0_2", "", "198.51.100.9:40000"))
	if len(peers) != tracker.MaxPeers {
		t.Fatalf("peers = %d entries, want the cap of %d", len(peers), tracker.MaxPeers)
	}
}

func TestExpiryOverHTTP(t *testing.T) {
	h := newMux(tracker.NewWithTimeout(20 * time.Millisecond))
	const addr = "203.0.113.7:51000"

	do(t, h, http.MethodPost, "/announce", announceBody(4711, "nginx-1.24.0_2"), addr)
	if rec := do(t, h, http.MethodPost, "/ping", "", addr); rec.Code != http.StatusOK {
		t.Fatal("peer not registered")
	}

	time.Sleep(40 * time.Millisecond)

	if peers := decodePeers(t, do(t, h, http.MethodGet, "/peers?pkg=nginx-1.24.0_2", "", addr)); len(peers) != 0 {
		t.Errorf("expired peer still returned: %v", peers)
	}
	if rec := do(t, h, http.MethodPost, "/ping", "", addr); rec.Code != http.StatusNotFound {
		t.Errorf("ping after expiry = %d, want 404", rec.Code)
	}
}

// Malformed input of every message type must be survived: the process keeps
// serving and answers with a status, never a panic or a hang. These cases are
// the seed corpus for the fuzz target.
func TestMalformedInput(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		target   string
		body     string
		wantCode int
	}{
		{"announce: bad json", http.MethodPost, "/announce", `{"servingPort":`, http.StatusBadRequest},
		{"announce: garbage", http.MethodPost, "/announce", "\x00\x01\x02 not json", http.StatusBadRequest},
		{"announce: empty body", http.MethodPost, "/announce", "", http.StatusBadRequest},
		{"announce: array at top level", http.MethodPost, "/announce", `[1,2,3]`, http.StatusBadRequest},
		{"announce: missing servingPort", http.MethodPost, "/announce", `{"packages":["nginx-1.24.0_2"]}`, http.StatusBadRequest},
		{"announce: port out of range", http.MethodPost, "/announce", `{"servingPort":70000,"packages":[]}`, http.StatusBadRequest},
		{"announce: negative port", http.MethodPost, "/announce", `{"servingPort":-1,"packages":[]}`, http.StatusBadRequest},
		{"announce: wrong type for port", http.MethodPost, "/announce", `{"servingPort":"4711","packages":[]}`, http.StatusBadRequest},
		{"announce: wrong type for packages", http.MethodPost, "/announce", `{"servingPort":4711,"packages":"nginx"}`, http.StatusBadRequest},
		{"announce: v0.1 body", http.MethodPost, "/announce", `{"peer_id":"d1","addr":"1.2.3.4:80","cids":[]}`, http.StatusBadRequest},
		{"announce: trailing data", http.MethodPost, "/announce", `{"servingPort":4711,"packages":[]} junk`, http.StatusBadRequest},
		{"announce: control chars in package", http.MethodPost, "/announce", `{"servingPort":4711,"packages":["nginx\n-1.0"]}`, http.StatusBadRequest},

		{"peers: missing pkg", http.MethodGet, "/peers", "", http.StatusBadRequest},
		{"peers: empty pkg", http.MethodGet, "/peers?pkg=", "", http.StatusBadRequest},
		{"peers: control chars in pkg", http.MethodGet, "/peers?pkg=nginx%00-1.0", "", http.StatusBadRequest},
		{"peers: oversized pkg", http.MethodGet, "/peers?pkg=" + strings.Repeat("a", proto.MaxNameVersionLen+1), "", http.StatusBadRequest},

		// Method routing: the surface is three paths, two verbs.
		{"announce via GET", http.MethodGet, "/announce", "", http.StatusMethodNotAllowed},
		{"peers via POST", http.MethodPost, "/peers?pkg=nginx-1.24.0_2", "", http.StatusMethodNotAllowed},
		{"unknown path", http.MethodGet, "/nope", "", http.StatusNotFound},
	}

	// One tracker across every case: the point is that it is still serving
	// correctly at the end.
	h := newMux(tracker.New())
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.target, tc.body, "203.0.113.7:51000")
			if rec.Code != tc.wantCode {
				t.Errorf("%s %s = %d, want %d (body %q)",
					tc.method, tc.target, rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}

	// Still alive and correct after all of that.
	if rec := do(t, h, http.MethodPost, "/announce", announceBody(4711, "nginx-1.24.0_2"), "203.0.113.7:51000"); rec.Code != http.StatusOK {
		t.Fatalf("tracker stopped serving after malformed input: %d", rec.Code)
	}
}

// Oversized input is 413, distinct from malformed input's 400: one message
// must not exhaust memory, and the sender should be able to tell the two
// failures apart.
func TestOversizedInputIs413(t *testing.T) {
	h := newMux(tracker.New())
	const addr = "203.0.113.7:51000"

	t.Run("body", func(t *testing.T) {
		// Valid JSON, just far too much of it -- so a 413 here proves
		// the size cap fired and not the parser.
		huge := `{"servingPort":4711,"packages":["` +
			strings.Repeat("a", proto.MaxBodyBytes) + `"]}`
		rec := do(t, h, http.MethodPost, "/announce", huge, addr)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("oversized body = %d, want 413", rec.Code)
		}
	})

	t.Run("package list", func(t *testing.T) {
		packages := make([]string, proto.MaxPackages+1)
		for i := range packages {
			packages[i] = fmt.Sprintf("pkg%d-1.0", i)
		}
		rec := do(t, h, http.MethodPost, "/announce", announceBody(4711, packages...), addr)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("oversized package list = %d, want 413", rec.Code)
		}
	})
}

// A handler panic must become a 500, not a dead process.
func TestPanicRecovery(t *testing.T) {
	h := recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := do(t, h, http.MethodGet, "/peers?pkg=nginx-1.24.0_2", "", "203.0.113.7:51000")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("panicking handler = %d, want 500", rec.Code)
	}
}

// Everything above drives the handler stack directly. Daemons are real HTTP
// clients on real sockets, so one pass over the wire is worth having: it is
// the only thing that exercises status-line and body serialisation.
func TestOverRealSocket(t *testing.T) {
	srv := httptest.NewServer(newMux(tracker.New()))
	t.Cleanup(srv.Close)

	// Unknown pinger.
	resp, err := http.Post(srv.URL+"/ping", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /ping: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("first ping = %d, want 404", resp.StatusCode)
	}

	// Announce, then find ourselves.
	resp, err = http.Post(srv.URL+"/announce", "application/json",
		strings.NewReader(announceBody(4711, "nginx-1.24.0_2")))
	if err != nil {
		t.Fatalf("POST /announce: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("announce = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/peers?pkg=nginx-1.24.0_2")
	if err != nil {
		t.Fatalf("GET /peers: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var out proto.PeerListResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if len(out.Peers) != 1 || out.Peers[0].Port != 4711 {
		t.Fatalf("peers = %+v, want one entry on port 4711", out.Peers)
	}
	// The loopback client's address is what got registered, and it must be
	// dialable as-is.
	if err := out.Peers[0].Validate(); err != nil {
		t.Errorf("peer %+v is not dialable: %v", out.Peers[0], err)
	}
}
