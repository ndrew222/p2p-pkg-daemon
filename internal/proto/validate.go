package proto

import (
	"errors"
	"fmt"
	"net"
	"regexp"
)

// Bounds used stop an unauthenticated peer from making the tracker allocate or loop without limit
const (
	MaxPeerIDLen = 64
	MaxAddrLen   = 253 + 6 // max DNS name + ":65535"
	MaxCIDs      = 4096
	CIDLen       = 64 // SHA-256, hex-encoded
)

var (
	ErrEmptyPeerID   = errors.New("proto: empty peer_id")
	ErrPeerIDTooLong = errors.New("proto: peer_id too long")
	ErrBadPeerID     = errors.New("proto: peer_id contains illegal characters")
	ErrEmptyAddr     = errors.New("proto: empty addr")
	ErrAddrTooLong   = errors.New("proto: addr too long")
	ErrBadAddr       = errors.New("proto: addr is not host:port")
	ErrTooManyCIDs   = errors.New("proto: too many cids")
	ErrBadCID        = errors.New("proto: malformed cid")
)

// peerIDPat restricts PeerIDs to a safe alphabet
// Keeps IDs out of log injection and path traversal territory
// regexp: regular expression, only accept specific characters
// Must: panic if it fails (process dies, no return an error or logging of warning)
var peerIDPat = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// cidPat is lowercase hex, exactly CIDLen characters
// only accept lower case, cause hex is case INsensitive, having exact same hex with lower and upper case will lead to 2 seperate entries
// so we set the default to only lower case
var cidPat = regexp.MustCompile(`^[0-9a-f]{64}$`)

// validatePeerID checks the identity field common to every request.
func validatePeerID(id PeerID) error {
	switch {
	case len(id) == 0:
		return ErrEmptyPeerID
	case len(id) > MaxPeerIDLen:
		return ErrPeerIDTooLong

	// only fires ErrBadPeerID if theres bad expression (input dont match peerIDPat)
	case !peerIDPat.MatchString(string(id)):
		return ErrBadPeerID
	}
	return nil
}

// validateAddr checks the dial target. Must be host:port, because F5 will hand this straight to a dialler
// Lower case: not expoerted
func validateAddr(addr string) error {
	switch {
	case len(addr) == 0:
		return ErrEmptyAddr
	case len(addr) > MaxAddrLen:
		return ErrAddrTooLong
	}
	// SplitHostPort takes in 1 string, return 3 things: (host, port, err)
	// fails when garbage is fed into it (eg empty host), failed parser = validation failed
	// use SplitHostPort instead of strings.Split because of IPv6 that seperates group by colons instead of dots in IPv4
	// _,_, discards the host and port, we only want the err. We only use the err value, keeping host and port used reurns compile error
	// statement before ; runs first, then condition is tested after
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return ErrBadAddr
	}
	return nil
}

// ValidateCID checks a single CID is syntactically well-formed
// It does NOT check the content exists (it is not known here) and is verified at fetch time by hashing the downloaded bytes
// Capitalised: exported so fetch loop and fuzzer (living in different packages) can access this
func ValidateCID(cid string) error {
	if !cidPat.MatchString(cid) {
		// Builds new error with values interpolated
		// %w (wrap verb) embeds error inside new error while preserving identity, using %v will flatten it to plain string and sever that link
		// %q: qouted string. Act as log-injection defence. Attacker's input can be clearly identified in log with ""
		// using %s means they can inject a line that looks like an actual log as it is not enclosed with ""
		return fmt.Errorf("%w: %q", ErrBadCID, cid)
	}
	return nil
}

// Validate checks a PingRequest is usable.
// *PringRequest pointer to ensure no copies every call, actual data is modified
// (r *PingRequest) is a receiver (r), attach Validate() to type PingRequest, making it a method
// Call using req.Validate() instead of Validate(req)
func (r *PingRequest) Validate() error {
	//r.PeerID is Go's way of dereferencing
	if err := validatePeerID(r.PeerID); err != nil {
		return err
	}
	return validateAddr(r.Addr)
}

// Validate checks an AnnounceRequest is usable.
func (r *AnnounceRequest) Validate() error {
	if err := validatePeerID(r.PeerID); err != nil {
		return err
	}
	if err := validateAddr(r.Addr); err != nil {
		return err
	}
	// check bound first before looping below
	// ensures attack cant inject large number of CIDs beyond bound
	if len(r.CIDs) > MaxCIDs {
		return ErrTooManyCIDs
	}

	// range yields index and element
	// _, is used to ignore index
	for _, cid := range r.CIDs {
		// returns first bad CID, dont collect all errors
		// no point enumerating the rest
		if err := ValidateCID(cid); err != nil {
			return err
		}
	}
	return nil
}
