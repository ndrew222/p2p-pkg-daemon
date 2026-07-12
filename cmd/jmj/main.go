package main

import (
	"flag"    // command line arg
	"log"     // output
	"strings" // strings.Split to turn "aaa, bbb, ccc" into ["aaa", "bbb", "ccc"]

	"github.com/ndrew222/p2p-pkg-daemon/internal/discovery" // F3 client
	"github.com/ndrew222/p2p-pkg-daemon/internal/proto"     // needed only for proto.PeerID type conversation
)

// TEMPORARY STUB: exists only to exercise internal/discovery end-to-end
// Owner of cmd/jmj: replace freely

func main() {
	// each flags below returns a *string, as command line hasnt been read yet, pointer points to those empty slots to be filled later
	tracker := flag.String("tracker", "http://127.0.0.1:8080", "tracker base URL")  // where the tracker lives, defaults to localhost:8080, so common case has no flag
	peerID := flag.String("id", "", "this peer's ID")                               // who we are, dafult is "", purposely invalid so it must be supplied
	addr := flag.String("addr", "", "this peer's host:port, where others reach us") // our own host:port, where other peer dials us
	cidList := flag.String("cids", "", "comma-separated CIDs we hold")              // whar we hold, real daemon reads this from disk

	// reads os.Args and fill those slots in
	flag.Parse()

	// flag check
	// os.Exit(1) for daemon with no identity as it cant ping and cant be found
	// fail here early
	if *peerID == "" || *addr == "" {
		log.Fatal("jmj: -id and -addr are required")
	}

	// Stand-in for the real CID store. Fixed at startup.
	// declares a nil slice, length and capcity 0, no backing array
	// can range, len and append to it even at zero. empty slice (nil) needs no initialisation in Go
	// handles empty case: strings.Split("","") returns [""] (a slice containing empty string, not empty slice). That single "" would be handed to Announce, hit ValidateCID(""), fail regex and whole announce rejected
	// a daemon holding nothing would not be able to register
	var cids []string
	if *cidList != "" {
		cids = strings.Split(*cidList, ",")
	}

	// The real daemon will read this from its content store
	currentCIDs := func() []string { return cids }

	// dereferencing the 3 flag pointers to actual strings
	c := discovery.New(*tracker, proto.PeerID(*peerID), *addr)

	// currentCIDs call the callback, get the slice and pass it
	// announce first before anything as daemon needs to be registered before lease renewal
	// if announce fails, it could be wrong tracker URL, tracker isnt running or own addr is malformed
	// retrying does not solve anything as they are config errors
	if err := c.Announce(currentCIDs()); err != nil {
		log.Fatalf("jmj: initial announce failed: %v", err)
	}

	// confirmation line
	log.Printf("jmj: peer=%q addr=%q running", *peerID, *addr)

	// Blocks forever, pinging on a ticker and re-announcing if the tracker forgets us
	// passes function currentCIDs as value
	// RunHeartBeat wamts the function so it can call later when it needs a fresh list
	// passing the parens currentCIDs() will pass a []string, where a func() []string is wanted
	c.RunHeartbeat(currentCIDs)
}
