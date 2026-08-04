package proto

import (
	"errors"
	"strings"
	"testing"
)

// The JSON in these tests is copied from v0.2 verbatim. If the spec's wire
// examples stop decoding, the struct tags have drifted from the contract.
func TestDecodeSpecBodies(t *testing.T) {
	t.Run("announce", func(t *testing.T) {
		const body = `{ "servingPort": 4711, "packages": ["nginx-1.24.0_2", "curl-8.6.0"] }`

		var req AnnounceRequest
		if err := Decode([]byte(body), &req); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if req.ServingPort != 4711 {
			t.Errorf("ServingPort = %d, want 4711", req.ServingPort)
		}
		if len(req.Packages) != 2 || req.Packages[0] != "nginx-1.24.0_2" {
			t.Errorf("Packages = %v", req.Packages)
		}
	})

	t.Run("peer list", func(t *testing.T) {
		const body = `{ "peers": [ { "ip": "203.0.113.7", "port": 4711 }, { "ip": "198.51.100.4", "port": 5522 } ] }`

		var resp PeerListResponse
		if err := Decode([]byte(body), &resp); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if len(resp.Peers) != 2 {
			t.Fatalf("got %d peers, want 2", len(resp.Peers))
		}
		if got := resp.Peers[0].Addr(); got != "203.0.113.7:4711" {
			t.Errorf("Addr() = %q, want 203.0.113.7:4711", got)
		}
	})

	t.Run("empty peer list", func(t *testing.T) {
		var resp PeerListResponse
		if err := Decode([]byte(`{"peers": []}`), &resp); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if len(resp.Peers) != 0 {
			t.Errorf("got %d peers, want 0", len(resp.Peers))
		}
	})

	t.Run("unknown pinger status", func(t *testing.T) {
		var resp StatusResponse
		if err := Decode([]byte(`{"status":"unknown"}`), &resp); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if resp.Status != StatusUnknown {
			t.Errorf("Status = %q, want %q", resp.Status, StatusUnknown)
		}
	})
}

// Malformed input of every shape must produce an error, never a panic. These
// double as the seed corpus for the tracker's fuzz target.
func TestDecodeMalformed(t *testing.T) {
	bodies := []struct {
		name string
		body string
	}{
		{"empty", ``},
		{"truncated", `{"servingPort": 4711, "packages": [`},
		{"garbage", `not json at all`},
		{"nul bytes", "\x00\x00\x00"},
		{"wrong type for port", `{"servingPort": "4711", "packages": []}`},
		{"wrong type for packages", `{"servingPort": 4711, "packages": "nginx-1.24.0_2"}`},
		{"array at top level", `[1,2,3]`},
		{"unknown field", `{"servingPort": 4711, "packages": [], "peer_id": "abc"}`},
		{"trailing data", `{"servingPort": 4711, "packages": []} junk`},
		{"two documents", `{"servingPort": 4711, "packages": []}{"servingPort": 1}`},
		{"deeply nested", strings.Repeat(`{"a":`, 500) + `1` + strings.Repeat(`}`, 500)},
	}

	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			var req AnnounceRequest
			if err := Decode([]byte(tc.body), &req); err == nil {
				t.Errorf("Decode(%q) = nil, want an error", tc.body)
			}
		})
	}
}

// peer_id and cids are v0.1 fields. DisallowUnknownFields means a v0.1 daemon
// talking to a v0.2 tracker is rejected loudly rather than silently registered
// with an empty package list.
func TestDecodeRejectsV01Announce(t *testing.T) {
	const v01 = `{"peer_id":"daemon-1","addr":"203.0.113.7:4711","cids":["` +
		`0000000000000000000000000000000000000000000000000000000000000000"]}`

	var req AnnounceRequest
	if err := Decode([]byte(v01), &req); err == nil {
		t.Fatal("v0.1 announce decoded into a v0.2 request, want an error")
	}
}

func TestDecodeTooLarge(t *testing.T) {
	oversized := make([]byte, MaxBodyBytes+1)
	var req AnnounceRequest
	if err := Decode(oversized, &req); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Decode(oversized) = %v, want ErrTooLarge", err)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	want := AnnounceRequest{
		ServingPort: 4711,
		Packages:    []string{"nginx-1.24.0_2", "curl-8.6.0", "py311-setuptools-63.1.0"},
	}

	body, err := Encode(&want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var got AnnounceRequest
	if err := Decode(body, &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.ServingPort != want.ServingPort || len(got.Packages) != len(want.Packages) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	for i := range want.Packages {
		if got.Packages[i] != want.Packages[i] {
			t.Errorf("Packages[%d] = %q, want %q", i, got.Packages[i], want.Packages[i])
		}
	}
}
