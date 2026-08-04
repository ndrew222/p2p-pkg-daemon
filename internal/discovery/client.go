// Package discovery is the daemon's side of the tracker conversation:
// tracker protocol v0.2, HTTP + JSON. Client is one-shot request/response;
// the heartbeat loop that drives it lives in KeepAlive.
package discovery

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/ndrew222/p2p-pkg-daemon/internal/peer"
	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
)

// PingInterval is how often the daemon renews its registration. It must be
// comfortably shorter than the tracker's TIMEOUT (60s): at 20s, two
// consecutive pings can be lost and the entry still survives.
const PingInterval = 20 * time.Second

// requestTimeout bounds one HTTP exchange. The tracker is on the far side of a
// network that may simply not answer.
const requestTimeout = 10 * time.Second

// ErrUnknownPeer is the 404 on /ping: the tracker has no entry for our IP,
// because it restarted or our deadline passed. It is a normal control signal,
// not a failure -- the caller's correct response is to announce. It has its
// own error value so callers can test for it with errors.Is rather than
// matching on a string.
var ErrUnknownPeer = errors.New("discovery: tracker does not know this peer")

// Client talks to one tracker on behalf of one daemon.
//
// It carries no identity. Under v0.2 the tracker keys state by the connection's
// source IP, so there is nothing to send and nothing to get wrong.
type Client struct {
	trackerURL string       // base, e.g. "http://127.0.0.1:8080"; methods append the path
	http       *http.Client // reused, carries the timeout
}

// Compile-time proof that Client is the seam both sides expect. These are the
// point of the v0.2 migration: before it, ValidateCID's 64-hex-char pattern
// made peer.PeerLister unsatisfiable by anything that speaks to a real
// tracker, and the mirror facade had no way to reach one.
var (
	_ Tracker         = (*Client)(nil)
	_ peer.PeerLister = (*Client)(nil)
)

// New returns a Client bound to one tracker.
func New(trackerURL string) *Client {
	return &Client{
		trackerURL: trackerURL,
		http:       &http.Client{Timeout: requestTimeout},
	}
}

// Announce registers our full package list. The list is a FULL REPLACEMENT,
// never a delta; an empty one deregisters us.
//
// servingPort must come from us: the tracker cannot infer it, because the
// source port of this outbound HTTP request has nothing to do with the port we
// listen on for peer transfers.
func (c *Client) Announce(servingPort int, packages []string) error {
	req := proto.AnnounceRequest{
		ServingPort: servingPort,
		Packages:    packages,
	}

	// Validate before sending. Never make the tracker reject what we could
	// have caught ourselves.
	if err := req.Validate(); err != nil {
		return fmt.Errorf("discovery: announce: %w", err)
	}

	body, err := proto.Encode(&req)
	if err != nil {
		return fmt.Errorf("discovery: announce: %w", err)
	}

	resp, err := c.http.Post(c.trackerURL+"/announce", "application/json", bytes.NewReader(body))
	if err != nil {
		// A transport error means the request never completed (DNS,
		// connection refused, timeout). It does not mean the tracker
		// said no.
		return fmt.Errorf("discovery: announce: %w", err)
	}
	// Draining and closing returns the connection to the pool. Skipping it
	// leaks a TCP connection per request until file descriptors run out.
	defer drain(resp)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discovery: announce: tracker returned %s", resp.Status)
	}

	log.Printf("discovery: announced port=%d packages=%d", servingPort, len(packages))
	return nil
}

// Ping renews our deadline. It returns ErrUnknownPeer if the tracker has
// forgotten us; the caller must then announce rather than retry the ping.
func (c *Client) Ping() error {
	// No body: v0.2 makes ping a bare keep-alive. http.NewRequest rather
	// than Post so we do not advertise a Content-Type for a body that does
	// not exist.
	req, err := http.NewRequest(http.MethodPost, c.trackerURL+"/ping", nil)
	if err != nil {
		return fmt.Errorf("discovery: ping: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("discovery: ping: %w", err)
	}
	defer drain(resp)

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrUnknownPeer
	default:
		return fmt.Errorf("discovery: ping: tracker returned %s", resp.Status)
	}
}

// Peers asks the tracker who holds nameVersion and returns dialable
// "host:port" addresses, in the tracker's order. This is the peer.PeerLister
// the mirror facade consumes.
//
// An empty result is not an error: no holder is a valid answer.
func (c *Client) Peers(nameVersion string) ([]string, error) {
	// Validate locally rather than round-tripping to find out.
	if err := proto.ValidateNameVersion(nameVersion); err != nil {
		return nil, fmt.Errorf("discovery: peers: %w", err)
	}

	// url.Values escapes the parameter. Naive concatenation would let a
	// name-version containing & or # inject query structure we never meant
	// to send.
	q := url.Values{}
	q.Set("pkg", nameVersion)

	resp, err := c.http.Get(c.trackerURL + "/peers?" + q.Encode())
	if err != nil {
		return nil, fmt.Errorf("discovery: peers: %w", err)
	}
	defer drain(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: peers: tracker returned %s", resp.Status)
	}

	// Bound the response. The tracker is not fully trusted either: it could
	// itself be compromised, and what comes back feeds a dialler.
	body, err := readLimited(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("discovery: peers: %w", err)
	}

	var out proto.PeerListResponse
	if err := proto.Decode(body, &out); err != nil {
		return nil, fmt.Errorf("discovery: peers: %w", err)
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("discovery: peers: %w", err)
	}

	addrs := make([]string, 0, len(out.Peers))
	for _, p := range out.Peers {
		addrs = append(addrs, p.Addr())
	}

	log.Printf("discovery: query pkg=%q -> %d peers", nameVersion, len(addrs))
	return addrs, nil
}

// readLimited reads at most MaxBodyBytes and then reports end of input, no
// matter how much the sender has left. Plain io.ReadAll reads until the sender
// stops; a sender that never stops is unbounded allocation.
func readLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, proto.MaxBodyBytes))
}

// drain empties and closes a response body so the connection can be reused.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, proto.MaxBodyBytes))
	resp.Body.Close()
}
