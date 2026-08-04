package proto

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateNameVersion(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		// The worked examples from v0.2.
		{"spec example", "nginx-1.24.0_2", nil},
		{"spec example 2", "curl-8.6.0", nil},
		{"hyphenated name", "py311-setuptools-63.1.0", nil},

		// v0.2 calls a package "an opaque string" and the tracker does
		// "exact match only". These are not well-formed name-versions and
		// the daemon's sanity filter drops them, but proto must NOT: the
		// wire layer has no grammar to enforce. If these ever start
		// failing, someone has invented a rule the spec does not have.
		{"no version part", "nginx", nil},
		{"version not digit-initial", "nginx-latest", nil},
		{"trailing hyphen", "nginx-", nil},
		{"unicode", "pkg-1.0-ü", nil},

		// What proto genuinely must reject.
		{"empty", "", ErrEmptyNameVersion},
		{"too long", strings.Repeat("a", MaxNameVersionLen+1), ErrNameVersionTooLong},
		{"exactly max", strings.Repeat("a", MaxNameVersionLen), nil},
		{"newline", "nginx-1.24.0_2\ntracker: forged log line", ErrBadNameVersion},
		{"carriage return", "nginx-1.24.0_2\r", ErrBadNameVersion},
		{"nul", "nginx\x00-1.0", ErrBadNameVersion},
		{"del", "nginx-1.0\x7f", ErrBadNameVersion},
		{"tab", "nginx\t1.0", ErrBadNameVersion},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNameVersion(tc.input)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ValidateNameVersion(%q) = %v, want %v", tc.input, err, tc.wantErr)
			}
		})
	}
}

func TestValidatePort(t *testing.T) {
	valid := []int{MinPort, 4711, 8080, MaxPort}
	for _, p := range valid {
		if err := ValidatePort(p); err != nil {
			t.Errorf("ValidatePort(%d) = %v, want nil", p, err)
		}
	}
	// 0 is "pick one for me" to a listener, never a thing to advertise.
	invalid := []int{0, -1, MaxPort + 1, 1 << 20}
	for _, p := range invalid {
		if err := ValidatePort(p); !errors.Is(err, ErrPortOutOfRange) {
			t.Errorf("ValidatePort(%d) = %v, want ErrPortOutOfRange", p, err)
		}
	}
}

func TestAnnounceRequestValidate(t *testing.T) {
	tooMany := make([]string, MaxPackages+1)
	for i := range tooMany {
		tooMany[i] = "nginx-1.24.0_2"
	}

	tests := []struct {
		name    string
		req     AnnounceRequest
		wantErr error
	}{
		{
			name: "spec example",
			req:  AnnounceRequest{ServingPort: 4711, Packages: []string{"nginx-1.24.0_2", "curl-8.6.0"}},
		},
		// The deregistration path (§POST /announce, "Empty list"). This
		// MUST validate -- it is how a daemon that just ran pkg clean
		// deregisters, so it has to reach the tracker to delete the entry.
		{
			name: "empty list deregisters",
			req:  AnnounceRequest{ServingPort: 4711, Packages: []string{}},
		},
		{
			name: "nil list deregisters",
			req:  AnnounceRequest{ServingPort: 4711},
		},
		{
			name:    "missing port",
			req:     AnnounceRequest{Packages: []string{"nginx-1.24.0_2"}},
			wantErr: ErrPortOutOfRange,
		},
		{
			name:    "port out of range",
			req:     AnnounceRequest{ServingPort: 70000, Packages: []string{"nginx-1.24.0_2"}},
			wantErr: ErrPortOutOfRange,
		},
		{
			name:    "one bad entry poisons the list",
			req:     AnnounceRequest{ServingPort: 4711, Packages: []string{"nginx-1.24.0_2", "", "curl-8.6.0"}},
			wantErr: ErrEmptyNameVersion,
		},
		{
			name:    "oversized list",
			req:     AnnounceRequest{ServingPort: 4711, Packages: tooMany},
			wantErr: ErrTooManyPackages,
		},
		{
			name:    "at the list bound",
			req:     AnnounceRequest{ServingPort: 4711, Packages: tooMany[:MaxPackages]},
			wantErr: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestPeerInfoValidate(t *testing.T) {
	tests := []struct {
		name    string
		peer    PeerInfo
		wantErr error
	}{
		{"spec example", PeerInfo{IP: "203.0.113.7", Port: 4711}, nil},
		{"ipv6", PeerInfo{IP: "2001:db8::1", Port: 5522}, nil},
		{"loopback", PeerInfo{IP: "127.0.0.1", Port: 8080}, nil},
		{"hostname is not an ip", PeerInfo{IP: "peer.example.com", Port: 4711}, ErrBadIP},
		{"empty ip", PeerInfo{Port: 4711}, ErrBadIP},
		{"host:port smuggled into ip", PeerInfo{IP: "203.0.113.7:4711", Port: 4711}, ErrBadIP},
		{"bad port", PeerInfo{IP: "203.0.113.7", Port: 0}, ErrPortOutOfRange},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.peer.Validate(); !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestPeerInfoAddr(t *testing.T) {
	// IPv6 must come back bracketed, or the dialler reads the last colon
	// group as the port.
	tests := []struct {
		peer PeerInfo
		want string
	}{
		{PeerInfo{IP: "203.0.113.7", Port: 4711}, "203.0.113.7:4711"},
		{PeerInfo{IP: "2001:db8::1", Port: 5522}, "[2001:db8::1]:5522"},
	}
	for _, tc := range tests {
		if got := tc.peer.Addr(); got != tc.want {
			t.Errorf("Addr() = %q, want %q", got, tc.want)
		}
	}
}

func TestPeerListResponseValidate(t *testing.T) {
	// A miss is an empty list, not an error.
	if err := (&PeerListResponse{Peers: []PeerInfo{}}).Validate(); err != nil {
		t.Errorf("empty peer list = %v, want nil", err)
	}

	good := &PeerListResponse{Peers: []PeerInfo{
		{IP: "203.0.113.7", Port: 4711},
		{IP: "198.51.100.4", Port: 5522},
	}}
	if err := good.Validate(); err != nil {
		t.Errorf("valid peer list = %v, want nil", err)
	}

	// A compromised tracker must not talk this daemon into dialling
	// something that is not an address.
	bad := &PeerListResponse{Peers: []PeerInfo{
		{IP: "203.0.113.7", Port: 4711},
		{IP: "not-an-ip", Port: 4711},
	}}
	if err := bad.Validate(); !errors.Is(err, ErrBadIP) {
		t.Errorf("poisoned peer list = %v, want ErrBadIP", err)
	}
}
