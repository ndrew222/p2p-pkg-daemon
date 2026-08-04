// Package proto is the daemon<->tracker wire contract: tracker protocol v0.2,
// HTTP + JSON. Packages are addressed by name-version strings (e.g.
// "nginx-1.24.0_2"). CIDs and peer_id are gone -- v0.2 keys tracker state by
// the connection's public IP and carries the peer's listen port in the
// announce body.
package proto

// AnnounceRequest is the body of POST /announce (v0.2 §POST /announce).
//
// There is no peer identity and no address field. The tracker takes the IP
// from the connection's source address and never from the body; ServingPort
// must come from the body because the source port of this outbound HTTP
// request is unrelated to the port the daemon listens on for peer transfers.
//
// Packages is a FULL REPLACEMENT, never a delta. An empty list is a valid
// announce: it deregisters the peer.
type AnnounceRequest struct {
	ServingPort int      `json:"servingPort"`
	Packages    []string `json:"packages"`
}

// PeerInfo is one holder in a GET /peers reply. IP and port only -- there is
// nothing else to say about a peer under v0.2.
type PeerInfo struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// PeerListResponse is the body of GET /peers?pkg=<name-version>. The query
// string is not echoed back; the caller already knows what it asked for.
//
// Peers is never nil on the wire: a miss is `{"peers": []}` with status 200,
// not a 404. The only 404 in this protocol is the unknown-pinger signal.
type PeerListResponse struct {
	Peers []PeerInfo `json:"peers"`
}

// StatusResponse is the small acknowledgement body the tracker attaches to
// /ping. `{"status":"unknown"}` on the 404 is specified; `{"status":"ack"}` on
// the 200 is permitted but not required.
type StatusResponse struct {
	Status string `json:"status"`
}

// The two status values that appear on the wire.
const (
	StatusAck     = "ack"
	StatusUnknown = "unknown"
)
