package daemon

// The upstream cross-check: does upstream_url agree with the repository pkg
// last actually fetched from?
//
// ADR-006 requires this and specifies it as ADVISORY. Everything here warns and
// nothing here refuses: if the comparison cannot be made, the daemon says
// nothing and carries on.
//
// Why it exists. Under ADR-005 the facade proxies the catalogue, so
// upstream_url does not merely supply fallback bytes -- it decides WHICH
// REPOSITORY pkg ends up with. pkg's own config points the jmj repository at
// loopback and says nothing about which real repository sits behind it, so
// pointing jmj at quarterly when the operator meant latest produces no error at
// all: pkg builds its database from whatever is proxied, every hash matches,
// and both branches carry the same FreeBSD signature. The only symptom is
// package versions being quietly wrong. This warning is the one thing that
// notices.
//
// Where the comparison value comes from. ADR-006 anticipated grepping pkg's
// UCL config for it. Measured on the reference host, there is a better source
// that needs no parsing at all: pkg records the upstream in the repository
// database itself, in the repodata table, ALREADY EXPANDED --
//
//	packagesite | pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly
//
// -- and that database is one the daemon already opens read-only. No UCL, no
// grep, no second file, and no guessing about pkg's multi-file shadowing rules.

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// UpstreamWarnings reports at most one warning: that the configured upstream
// corresponds to NONE of the repositories pkg actually fetches from.
//
// "None of them" rather than "not all of them", and that distinction was
// measured rather than reasoned. A stock FreeBSD 15.1 host has TWO enabled
// repositories with different URLs --
//
//	FreeBSD-ports        .../FreeBSD:15:amd64/quarterly
//	FreeBSD-ports-kmods  .../FreeBSD:15:amd64/kmods_quarterly_1
//
// -- so a per-catalogue comparison warns about kmods on every start of a
// correctly configured daemon. A warning that always fires on a correct setup
// is worse than no warning: it teaches the operator to ignore the one case
// this exists to catch. jmj has one upstream and a stock host has several
// repositories; a catalogue from a different repository is not evidence of
// misconfiguration.
//
// If at least one catalogue agrees, the configured value names a repository
// pkg really uses and nothing is said. If none does, the operator has almost
// certainly typed the wrong branch or ABI -- which produces no other symptom,
// because pkg builds its database from whatever is proxied and every hash
// then matches.
//
// configured is expected to be fully expanded already (config.ExpandUpstream
// runs first), which matches repodata's stored form.
func UpstreamWarnings(configured string, sources map[string]string) []string {
	want, ok := normaliseRepoURL(configured)
	if !ok {
		return nil
	}

	var known []string
	for _, raw := range sources {
		got, ok := normaliseRepoURL(raw)
		if !ok {
			continue
		}
		// A loopback source is this daemon: once the operator has switched
		// pkg over to jmj, pkg records OUR address here, so the row says
		// nothing about which real repository is behind it. Treating that
		// as a disagreement would warn on every start after a successful
		// switch.
		//
		// The check therefore does its work at the moment it is useful: the
		// first start after switching, when the catalogue on disk is still
		// the one the stock configuration fetched.
		if isLoopbackURL(got) {
			continue
		}
		if got == want {
			return nil
		}
		known = append(known, raw)
	}

	if len(known) == 0 {
		return nil
	}
	sort.Strings(known)
	return []string{fmt.Sprintf(
		"upstream_url is %q, which matches none of the repositories pkg last fetched from (%s). jmj is pkg's only mirror and proxies the catalogue, so a wrong branch or ABI here silently changes which repository packages come from -- versions differ with no error anywhere. Check the value, or ignore this if you are deliberately switching repositories",
		configured, strings.Join(known, ", "))}
}

// normaliseRepoURL puts a repository URL in a comparable form: pkg's "pkg+"
// scheme prefix removed (repodata stores "pkg+https://..."), host lowercased,
// and any trailing slash dropped so that ".../quarterly" and ".../quarterly/"
// are not reported as a mismatch.
func normaliseRepoURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.TrimPrefix(u.Scheme, "pkg+")
	path := strings.TrimSuffix(u.Path, "/")
	return scheme + "://" + strings.ToLower(u.Host) + path, true
}

func isLoopbackURL(normalised string) bool {
	u, err := url.Parse(normalised)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
