package discovery

import (
	"bytes"    // for bytes.NewReader, turns []byte into stream as http.Post wants a stream
	"errors"   // errors.New for sentinel, errors.Is for comparing against it
	"fmt"      // fmt.Errorf for wrappting errors with context
	"io"       // io.ReadAll and io.LimitReader
	"log"      // for sequence diagram arrows
	"net/http" // HTTP client, not the server
	"net/url"  // safely building a query string
	"time"     // ticker and client timeout

	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"
)

// PingInterval is how often the daemon renews its lease
// Must be comfortably shorter than the tracker's LeaseTTL (60s)
// not set at 60s as one slow ping and daemon expires, 20s allows for 2 consecutive faild pings and daemon still survives
const PingInterval = 20 * time.Second

// ErrUnknownPeer means the tracker has no record of us, it restarted, or our lease expired. The caller must reannounce.
var ErrUnknownPeer = errors.New("discovery: tracker does not know this peer")

// Client talks to the tracker on behalf of one daemon.
type Client struct {
	trackerURL string       // e.g. "http://127.0.0.1:8080". The base, methods append /announce, /ping, /peers
	peerID     proto.PeerID // our identity
	addr       string       // our own host:port, where peers reach us
	http       *http.Client // reused; carries the timeout
}

// New returns a Client bound to one tracker and one identity
// takes three things that vary per daemon, builds HTTP client internally with timeout baked in
// returns *Client so http.Client isnt copied around
func New(trackerURL string, peerID proto.PeerID, addr string) *Client {
	return &Client{
		trackerURL: trackerURL,
		peerID:     peerID,
		addr:       addr,
		http:       &http.Client{Timeout: 10 * time.Second},
	}
}

// Announce registers our full CID list with the tracker
// Builds request struct from our identity plus callers CID list
// c.peerID and c.addr come from client. caller dont supply them and cant get them wrong
func (c *Client) Announce(cids []string) error {
	req := proto.AnnounceRequest{
		PeerID: c.peerID,
		Addr:   c.addr,
		CIDs:   cids,
	}

	// Validate before sending. Never make the tracker reject what we could have caught ourselves.
	// catches any malformed addr (typo or empty flag for example)
	// %w wraps so errors.Is still works on whatever came out of Validate
	if err := req.Validate(); err != nil {
		return fmt.Errorf("discovery: announce: %w", err)
	}

	// struct to JSON bytes
	body, err := proto.Encode(&req)
	if err != nil {
		return fmt.Errorf("discovery: announce: %w", err)
	}

	// bytes.NewReader(): post wants an io.Reader(a stream), but we have a []byte(block)
	// application/json is the content type header, tells the tracker how to interpret the bytes
	// returns (*https.Response, error)
	// error means request never completed (eg DNS failed, connection refused, timeout etc), dont mean tracker returned an error status
	resp, err := c.http.Post(c.trackerURL+"/announce",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("discovery: announce: %w", err)
	}
	defer resp.Body.Close() // close response body, else it leaks underlying TCP connecion, never returns to pool and never gets reused. Then file descriptors get exhuasted and every sebsequent request fails

	// tracker's handlers returns 204 No content on sucess, anything else is a failure
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("discovery: announce: tracker returned %s", resp.Status)
	}

	// sequence diagram arrow, client side
	log.Printf("discovery: announced peer=%q cids=%d", c.peerID, len(cids))
	return nil
}

// Ping renews our lease. Returns ErrUnknownPeer if the tracker has
// forgotten us; the caller should then re-announce.
func (c *Client) Ping() error {
	req := proto.PingRequest{
		PeerID: c.peerID,
		Addr:   c.addr,
	}

	if err := req.Validate(); err != nil {
		return fmt.Errorf("discovery: ping: %w", err)
	}

	body, err := proto.Encode(&req)
	if err != nil {
		return fmt.Errorf("discovery: ping: %w", err)
	}

	resp, err := c.http.Post(c.trackerURL+"/ping",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("discovery: ping: %w", err)
	}
	defer resp.Body.Close()

	// 204: lease renewed
	// 404: tracker dont know us, return the sentinel ErrUnkownPeer, not a wrapped generic error
	// anything else: genuinely wrong, generic error
	// 404 gets its own error value for caller to detect it specifically instead of doing string matching to tell it apart from other failures. A sentinel allows one to compare agaisnt errors.Is
	switch resp.StatusCode {
	case http.StatusNoContent:
		log.Printf("discovery: ping ok peer=%q", c.peerID)
		return nil
	case http.StatusNotFound:
		return ErrUnknownPeer
	default:
		return fmt.Errorf("discovery: ping: tracker returned %s", resp.Status)
	}
}

// Peers asks the tracker who holds cid, valiate before sending, rather than round-tripping to find out any bad input that should be caught locally
// returns ([]proto.PeerInfo, error). failure returns (nil, error)
func (c *Client) Peers(cid string) ([]proto.PeerInfo, error) {
	if err := proto.ValidateCID(cid); err != nil {
		return nil, fmt.Errorf("discovery: peers: %w", err)
	}

	// url.Values escapes the query string properly
	// url.Values is a map of query parameters. Encode() serialises it to cid=abc123
	// if cid contained &,=,#, space or newline, naive concantation would inject those into URL structure, meaning attacker added a parameter we never intended
	q := url.Values{}
	q.Set("cid", cid)
	target := c.trackerURL + "/peers?" + q.Encode()

	// Get instead of Post as theres no body to send
	// check for transport error then status code
	// 200 code is accepted here as there is a body
	resp, err := c.http.Get(target)
	if err != nil {
		return nil, fmt.Errorf("discovery: peers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: peers: tracker returned %s", resp.Status)
	}

	// Bound the response. The tracker is not fully trusted either incase tracker itself is compromised.
	body, err := readLimited(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("discovery: peers: %w", err)
	}

	var out proto.PeerListResponse
	if err := proto.Decode(body, &out); err != nil {
		return nil, fmt.Errorf("discovery: peers: %w", err)
	}

	// returns just the peers, caller only asked for who has the CID, no need for echoed CID
	log.Printf("discovery: query cid=%q -> %d peers", cid, len(out.Peers))
	return out.Peers, nil
}

// RunHeartbeat blocks, pinging on a ticker. Re-announces if the tracker has forgotten us. Run it in a goroutine.
// cids is a callback so the daemon can report its current CID list at re-announce time, not a stale snapshot from startup.
// parameter is a function, that when called returns the current CID list, passing it as function instead of string because CID list changes overtime, simply passing as []string only returns a snapshot at startup
func (c *Client) RunHeartbeat(cids func() []string) {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()

	// .C is a channel, every 20s a value arrives
	// for range receives forever
	// between ticks, the goroutine is parked (descheduled, no CPU consumption)
	for range ticker.C {
		err := c.Ping()
		if err == nil {
			continue // lease renewed, nothing to do, wait for next tick
		}

		//errors.Is walks the wrap chain
		// cids() calls the callback, it fetch the current list right now and announce that
		if errors.Is(err, ErrUnknownPeer) {
			log.Printf("discovery: tracker forgot us; re-announcing") // self healing, tracker can be killed and daemon independently notices within 20s and re-registers itself. Rebuild the list
			if err := c.Announce(cids()); err != nil {
				log.Printf("discovery: re-announce failed: %v", err) // if reannounce fails, log it and continue. It retries forever at every new tick.
			}
			continue
		}

		// Network error, tracker down, etc. Log and keep trying.
		// this function never returns, it is blocked forever by design in case tracker is unreachable which is to be expected.
		log.Printf("discovery: ping failed: %v", err)
	}

}

// readLimited reads at most 1 MiB from r then reports end of input no matter how much sender has left to send
// otherwise io.ReadAll reads until sender stops. If sender never stops, read never stops, allocating forever, leads to memory exhaustion
func readLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, 1<<20))
}
