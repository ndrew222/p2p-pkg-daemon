package daemon

import (
	"strings"
	"testing"
)

// The URL forms here are the ones measured on the reference host
// (FreeBSD 15.1-RELEASE-p1 / pkg 2.7.5), including pkg's "pkg+" scheme prefix
// and the fact that repodata stores the ABI already expanded.
const (
	portsSource = "pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly"
	portsPath   = "/var/db/pkg/repos/FreeBSD-ports/db"
	kmodsSource = "pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/kmods_quarterly_1"
	kmodsPath   = "/var/db/pkg/repos/FreeBSD-ports-kmods/db"
)

func TestUpstreamWarnings(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		sources    map[string]string
		wantWarn   bool
	}{
		{
			name:       "agrees once the pkg+ prefix is discounted",
			configured: "https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly",
			sources:    map[string]string{portsPath: portsSource},
		},
		{
			name:       "a trailing slash is not a mismatch",
			configured: "https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly/",
			sources:    map[string]string{portsPath: portsSource},
		},
		{
			name:       "host case is not a mismatch",
			configured: "https://pkg.freebsd.org/FreeBSD:15:amd64/quarterly",
			sources:    map[string]string{portsPath: portsSource},
		},
		{
			// The failure this whole check exists for: nothing else in the
			// system notices, because both branches are signed identically.
			name:       "wrong branch warns",
			configured: "https://pkg.FreeBSD.org/FreeBSD:15:amd64/latest",
			sources:    map[string]string{portsPath: portsSource},
			wantWarn:   true,
		},
		{
			name:       "wrong ABI warns",
			configured: "https://pkg.FreeBSD.org/FreeBSD:14:amd64/quarterly",
			sources:    map[string]string{portsPath: portsSource},
			wantWarn:   true,
		},
		{
			// After the operator switches pkg over to jmj, pkg records OUR
			// loopback address here. Warning then would fire on every start
			// and train the operator to ignore the one warning that matters.
			name:       "a loopback source is us, and is skipped",
			configured: "https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly",
			sources:    map[string]string{portsPath: "http://127.0.0.1:9001/FreeBSD:15:amd64/quarterly"},
		},
		{
			name:       "localhost is skipped too",
			configured: "https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly",
			sources:    map[string]string{portsPath: "http://localhost:9001/x"},
		},
		{
			// Advisory means advisory: an unreadable or absent source is
			// silence, never a complaint.
			name:       "no sources at all is silent",
			configured: "https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly",
			sources:    map[string]string{},
		},
		{
			name:       "an unparseable source is skipped, not reported",
			configured: "https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly",
			sources:    map[string]string{portsPath: "not a url"},
		},
		{
			name:       "an unparseable configured value is silent",
			configured: "",
			sources:    map[string]string{portsPath: portsSource},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := UpstreamWarnings(tc.configured, tc.sources)
			if tc.wantWarn && len(got) == 0 {
				t.Fatalf("UpstreamWarnings(%q, %v) = no warnings, want one", tc.configured, tc.sources)
			}
			if !tc.wantWarn && len(got) != 0 {
				t.Fatalf("UpstreamWarnings(%q, %v) = %v, want none", tc.configured, tc.sources, got)
			}
			// A warning nobody can act on is noise: it must carry the
			// configured value and what was found instead.
			if tc.wantWarn {
				w := got[0]
				for _, must := range []string{tc.configured, "quarterly"} {
					if !strings.Contains(w, must) {
						t.Errorf("warning %q does not mention %q", w, must)
					}
				}
			}
		})
	}
}

// THE REGRESSION TEST FOR THE REAL-HOST FINDING.
//
// A stock FreeBSD 15.1 host has two enabled repositories with different URLs.
// An earlier version of this check compared per catalogue and therefore warned
// about kmods on every start of a CORRECTLY configured daemon -- caught only by
// running it against the real host, never by the table above.
//
// One catalogue agreeing is enough: the configured value names a repository pkg
// really uses. A warning that always fires trains the operator to ignore the
// one case this exists to catch.
func TestUpstreamWarningsIsSilentOnAStockTwoRepositoryHost(t *testing.T) {
	sources := map[string]string{portsPath: portsSource, kmodsPath: kmodsSource}

	if got := UpstreamWarnings("https://pkg.FreeBSD.org/FreeBSD:15:amd64/quarterly", sources); len(got) != 0 {
		t.Errorf("a correct upstream on a stock two-repository host warned: %v", got)
	}
	// Pointing at the kmods repository is equally coherent, and equally quiet.
	if got := UpstreamWarnings("https://pkg.FreeBSD.org/FreeBSD:15:amd64/kmods_quarterly_1", sources); len(got) != 0 {
		t.Errorf("matching the kmods repository warned: %v", got)
	}
	// Matching neither is the case worth speaking up about, and the warning
	// must name both real repositories so the operator can see the options.
	got := UpstreamWarnings("https://pkg.FreeBSD.org/FreeBSD:15:amd64/latest", sources)
	if len(got) != 1 {
		t.Fatalf("got %d warnings, want exactly 1: %v", len(got), got)
	}
	for _, must := range []string{"quarterly", "kmods_quarterly_1"} {
		if !strings.Contains(got[0], must) {
			t.Errorf("warning %q does not name %q", got[0], must)
		}
	}
}
