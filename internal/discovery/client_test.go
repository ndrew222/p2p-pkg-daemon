package discovery

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
)

// recordingTracker is a stand-in tracker that records what arrived and replies
// with whatever the test dictates. The point is to pin what the client puts on
// the wire, not to re-test the real tracker.
type recordingTracker struct {
	lastPath   string
	lastQuery  string
	lastBody   string
	lastMethod string

	status int    // reply status, 200 if zero
	body   string // reply body
}

func (rt *recordingTracker) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rt.lastMethod = r.Method
		rt.lastPath = r.URL.Path
		rt.lastQuery = r.URL.RawQuery

		body := make([]byte, 1<<16)
		n, _ := r.Body.Read(body)
		rt.lastBody = string(body[:n])

		status := rt.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(rt.body))
	}
}

func newTestClient(t *testing.T, rt *recordingTracker) *Client {
	t.Helper()
	srv := httptest.NewServer(rt.handler())
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestAnnounceSendsV02Body(t *testing.T) {
	rt := &recordingTracker{body: `{"status":"ack"}`}
	c := newTestClient(t, rt)

	if err := c.Announce(4711, []string{"nginx-1.24.0_2", "curl-8.6.0"}); err != nil {
		t.Fatalf("Announce: %v", err)
	}

	if rt.lastMethod != http.MethodPost || rt.lastPath != "/announce" {
		t.Errorf("sent %s %s, want POST /announce", rt.lastMethod, rt.lastPath)
	}
	// The body must be exactly the v0.2 shape: no peer_id, no addr.
	var got proto.AnnounceRequest
	if err := proto.Decode([]byte(rt.lastBody), &got); err != nil {
		t.Fatalf("tracker could not decode what we sent (%q): %v", rt.lastBody, err)
	}
	if got.ServingPort != 4711 {
		t.Errorf("servingPort = %d, want 4711", got.ServingPort)
	}
	if len(got.Packages) != 2 {
		t.Errorf("packages = %v, want two entries", got.Packages)
	}
	for _, banned := range []string{"peer_id", "addr", "cids"} {
		if strings.Contains(rt.lastBody, banned) {
			t.Errorf("announce body still carries the v0.1 field %q: %s", banned, rt.lastBody)
		}
	}
}

// An empty list is the deregistration path and must actually be sent.
func TestAnnounceEmptyListIsSent(t *testing.T) {
	rt := &recordingTracker{body: `{"status":"ack"}`}
	c := newTestClient(t, rt)

	if err := c.Announce(4711, nil); err != nil {
		t.Fatalf("empty Announce: %v", err)
	}
	if rt.lastPath != "/announce" {
		t.Fatal("empty announce was not sent")
	}
}

// Catch locally what the tracker would reject anyway.
func TestAnnounceValidatesBeforeSending(t *testing.T) {
	rt := &recordingTracker{}
	c := newTestClient(t, rt)

	if err := c.Announce(0, []string{"nginx-1.24.0_2"}); !errors.Is(err, proto.ErrPortOutOfRange) {
		t.Errorf("Announce(port 0) = %v, want ErrPortOutOfRange", err)
	}
	if err := c.Announce(4711, []string{"nginx-1.24.0_2\n"}); !errors.Is(err, proto.ErrBadNameVersion) {
		t.Errorf("Announce(control chars) = %v, want ErrBadNameVersion", err)
	}
	if rt.lastPath != "" {
		t.Errorf("an invalid announce reached the tracker at %q", rt.lastPath)
	}
}

// Named for the Client to keep it apart from keepalive_test.go's TestPing,
// which covers the loop that calls this.
func TestClientPing(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{"registered", http.StatusOK, `{"status":"ack"}`, nil},
		// The load-bearing 404. It must come back as its own value so
		// KeepAlive can tell it apart from a real failure and announce.
		{"forgotten", http.StatusNotFound, `{"status":"unknown"}`, ErrUnknownPeer},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &recordingTracker{status: tc.status, body: tc.body}
			c := newTestClient(t, rt)

			err := c.Ping()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Ping() = %v, want %v", err, tc.wantErr)
			}
			if rt.lastMethod != http.MethodPost || rt.lastPath != "/ping" {
				t.Errorf("sent %s %s, want POST /ping", rt.lastMethod, rt.lastPath)
			}
			// v0.2: ping is a bare keep-alive.
			if rt.lastBody != "" {
				t.Errorf("ping carried a body: %q", rt.lastBody)
			}
		})
	}
}

// A 500 is a real failure, not the announce-yourself signal. Confusing the two
// would have KeepAlive re-announcing into a broken tracker on every beat.
func TestClientPingServerErrorIsNotUnknownPeer(t *testing.T) {
	rt := &recordingTracker{status: http.StatusInternalServerError}
	c := newTestClient(t, rt)

	err := c.Ping()
	if err == nil {
		t.Fatal("Ping() on a 500 = nil, want an error")
	}
	if errors.Is(err, ErrUnknownPeer) {
		t.Error("Ping() on a 500 = ErrUnknownPeer, want a plain error")
	}
}

func TestPeers(t *testing.T) {
	rt := &recordingTracker{
		body: `{"peers":[{"ip":"203.0.113.7","port":4711},{"ip":"2001:db8::1","port":5522}]}`,
	}
	c := newTestClient(t, rt)

	addrs, err := c.Peers("nginx-1.24.0_2")
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}

	if rt.lastMethod != http.MethodGet || rt.lastPath != "/peers" {
		t.Errorf("sent %s %s, want GET /peers", rt.lastMethod, rt.lastPath)
	}
	// v0.2 renamed the parameter; ?cid= would silently return nothing.
	if rt.lastQuery != "pkg=nginx-1.24.0_2" {
		t.Errorf("query = %q, want pkg=nginx-1.24.0_2", rt.lastQuery)
	}

	// The IPv6 entry must come back bracketed, or a dialler reads the last
	// colon group as the port.
	want := []string{"203.0.113.7:4711", "[2001:db8::1]:5522"}
	if len(addrs) != len(want) {
		t.Fatalf("addrs = %v, want %v", addrs, want)
	}
	for i := range want {
		if addrs[i] != want[i] {
			t.Errorf("addrs[%d] = %q, want %q", i, addrs[i], want[i])
		}
	}
}

// No holder is a valid answer, not a failure. The facade turns an empty list
// into a 404 so pkg falls through to its next mirror.
func TestPeersEmptyIsNotAnError(t *testing.T) {
	rt := &recordingTracker{body: `{"peers":[]}`}
	c := newTestClient(t, rt)

	addrs, err := c.Peers("nothing-1.0")
	if err != nil {
		t.Fatalf("Peers on a miss = %v, want nil", err)
	}
	if len(addrs) != 0 {
		t.Errorf("addrs = %v, want empty", addrs)
	}
}

// The tracker feeds a dialler, so a compromised one must not be able to point
// this daemon at something that is not an address.
func TestPeersRejectsPoisonedResponse(t *testing.T) {
	bodies := []string{
		`{"peers":[{"ip":"not-an-ip","port":4711}]}`,
		`{"peers":[{"ip":"203.0.113.7","port":0}]}`,
		`{"peers":[{"ip":"203.0.113.7","port":99999}]}`,
		`{"peers":[{"ip":"","port":4711}]}`,
		`{"peers":[{"ip":"203.0.113.7","port":4711},{"ip":"evil.example.com","port":80}]}`,
	}

	for _, body := range bodies {
		rt := &recordingTracker{body: body}
		c := newTestClient(t, rt)

		if _, err := c.Peers("nginx-1.24.0_2"); err == nil {
			t.Errorf("Peers accepted a poisoned reply: %s", body)
		}
	}
}

func TestPeersValidatesQueryLocally(t *testing.T) {
	rt := &recordingTracker{body: `{"peers":[]}`}
	c := newTestClient(t, rt)

	if _, err := c.Peers(""); !errors.Is(err, proto.ErrEmptyNameVersion) {
		t.Errorf("Peers(\"\") = %v, want ErrEmptyNameVersion", err)
	}
	if rt.lastPath != "" {
		t.Errorf("an invalid query reached the tracker at %q", rt.lastPath)
	}
}

// A transport failure must surface, not be mistaken for an empty peer list --
// the facade answers 502 for the former and 404 for the latter, and pkg
// behaves differently for each.
func TestPeersTrackerUnreachable(t *testing.T) {
	c := New("http://127.0.0.1:1")

	if _, err := c.Peers("nginx-1.24.0_2"); err == nil {
		t.Fatal("Peers against a dead tracker = nil error")
	}
}
