package proto

// PeerID identifies a peer across the network
type PeerID string

// PingRequest is the liveness heartbeat from daemon -> tracker
// Carries no CIDs, only renews the peer's lease (timer)
type PingRequest struct {
	PeerID PeerID `json:"peer_id"`
	Addr   string `json:"addr"`
}

// AnnounceRequest registers a peer's full CID list: daemon -> tracker.
type AnnounceRequest struct {
	PeerID PeerID   `json:"peer_id"`
	Addr   string   `json:"addr"`
	CIDs   []string `json:"cids"`
}

// PeerInfo is one holder, returned to a querying daemon.
type PeerInfo struct {
	PeerID PeerID `json:"peer_id"`
	Addr   string `json:"addr"`
}

// PeerListResponse answers GET /peers?cid=x
type PeerListResponse struct {
	CID   string     `json:"cid"`
	Peers []PeerInfo `json:"peers"`
}
