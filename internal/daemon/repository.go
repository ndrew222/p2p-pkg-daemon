package daemon

// Repository is one reader over pkg's repository database, supplying both
// views of a package row: the expected hash and the expected file size.
//
// The two halves stay declared separately -- PackageHashes in facade.go,
// RepositoryDatabase in watcher.go -- and that is deliberate, not leftover.
// The announce path must never hash (AGENTS.md: "No hashing at announce time"),
// and SanityFilter taking a size-only interface is what *proves* it cannot: the
// signature carries the guarantee, so it survives a reader who does not know
// the rule. Collapsing both into a single struct-returning method would hand
// the watcher a hash it is merely trusted to ignore.
//
// The facade holds the composite because the peer transfer spec requires the
// hash and the exact size to arrive together -- "an implementation that has one
// and not the other is a bug, not a case to handle gracefully". One value
// carrying both makes that state unrepresentable on the fetch path.
//
// Decided by the owner; see HANDOFF.md §4.3.
type Repository interface {
	PackageHashes
	RepositoryDatabase
}
