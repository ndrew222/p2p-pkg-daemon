package proto

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
)

// Bounds stop an unauthenticated peer from making the tracker allocate or loop
// without limit. The spec leaves MAX_ANNOUNCE_LEN and MAX_BODY_BYTES
// implementation-chosen but requires them to be documented, so they are named
// and exported here rather than buried in a handler.
const (
	// MaxNameVersionLen bounds one name-version string. Name-versions are
	// derived from cache filenames, so POSIX NAME_MAX is the natural cap.
	MaxNameVersionLen = 255

	// MaxPackages is MAX_ANNOUNCE_LEN: the cap on an announce list.
	MaxPackages = 4096

	// MaxBodyBytes is MAX_BODY_BYTES: the cap on a request body. The tracker
	// enforces it with http.MaxBytesReader before reading, and Decode
	// enforces it again on bytes already in hand.
	MaxBodyBytes = 1 << 20 // 1 MiB

	// MinPort and MaxPort bound servingPort. Port 0 means "pick one for me"
	// to a listener, which is never a valid thing to advertise to peers.
	MinPort = 1
	MaxPort = 65535
)

var (
	ErrEmptyNameVersion   = errors.New("proto: empty name-version")
	ErrNameVersionTooLong = errors.New("proto: name-version too long")
	ErrBadNameVersion     = errors.New("proto: name-version contains illegal characters")
	ErrTooManyPackages    = errors.New("proto: too many packages")
	ErrPortOutOfRange     = errors.New("proto: servingPort out of range")
	ErrBadIP              = errors.New("proto: peer ip is not an IP address")
)

// controlChars matches C0 controls and DEL. These are the only characters a
// name-version may not contain.
//
// DELIBERATELY PERMISSIVE. v0.2 §"What the tracker is" calls a package "an
// opaque string of the form name-version" and §GET /peers specifies "exact
// match only -- no prefix, no fuzzy matching". The tracker matches strings; it
// does not parse them. No spec anywhere in docs/ defines the name-version
// grammar, so this validator refuses to invent one and checks only what the
// layers below actually need: non-empty (it is a map key), bounded (memory),
// and free of control characters (these strings reach log lines).
//
// The structural rule -- split on the last hyphen, version must start with a
// digit -- is a daemon-side sanity filter and lives in
// internal/daemon.parsePackageName, which is where v0.2 §"Daemon-side
// obligations" puts it. Do not duplicate it here; a peer is allowed to
// announce a string this daemon would not have generated.
var controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// ValidateNameVersion checks one package identifier is safe to put on the wire,
// use as a map key, and write to a log.
func ValidateNameVersion(nameVersion string) error {
	switch {
	case len(nameVersion) == 0:
		return ErrEmptyNameVersion
	case len(nameVersion) > MaxNameVersionLen:
		return fmt.Errorf("%w: %d bytes", ErrNameVersionTooLong, len(nameVersion))
	case controlChars.MatchString(nameVersion):
		// %q so an injected newline shows as \n in the log rather than
		// forging a log line.
		return fmt.Errorf("%w: %q", ErrBadNameVersion, nameVersion)
	}
	return nil
}

// ValidatePort checks a TCP port is one a peer could actually be reached on.
func ValidatePort(port int) error {
	if port < MinPort || port > MaxPort {
		return fmt.Errorf("%w: %d", ErrPortOutOfRange, port)
	}
	return nil
}

// Validate checks an AnnounceRequest is usable.
//
// An empty Packages list is VALID: v0.2 §POST /announce makes the empty
// announce the deregistration path, so it must pass validation and reach the
// tracker, which deletes the entry.
func (r *AnnounceRequest) Validate() error {
	if err := ValidatePort(r.ServingPort); err != nil {
		return err
	}

	// Check the bound before the loop, so an oversized list is rejected
	// without walking it.
	if len(r.Packages) > MaxPackages {
		return fmt.Errorf("%w: %d > %d", ErrTooManyPackages, len(r.Packages), MaxPackages)
	}

	for _, nameVersion := range r.Packages {
		// Return the first bad entry; enumerating the rest tells the
		// caller nothing it can act on.
		if err := ValidateNameVersion(nameVersion); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks one peer is dialable. net.ParseIP rather than a string check
// because the caller hands IP straight to a dialler.
func (p *PeerInfo) Validate() error {
	if net.ParseIP(p.IP) == nil {
		return fmt.Errorf("%w: %q", ErrBadIP, p.IP)
	}
	return ValidatePort(p.Port)
}

// Validate checks a peer list before the daemon dials anything in it. The
// tracker is not fully trusted either -- it could be compromised, and its
// reply feeds a dialler.
func (r *PeerListResponse) Validate() error {
	for i := range r.Peers {
		if err := r.Peers[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Addr renders a peer as the "host:port" string a dialler wants.
// net.JoinHostPort brackets IPv6 literals, which plain concatenation does not.
func (p PeerInfo) Addr() string {
	return net.JoinHostPort(p.IP, strconv.Itoa(p.Port))
}
