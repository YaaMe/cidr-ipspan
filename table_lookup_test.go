package ipspan

import (
	"math/rand"
	"net/netip"
	"testing"
)

// longestLinear is the definition Lookup optimises: among all prefixes
// containing the address, the one with the most bits.
func longestLinear(prefixes []netip.Prefix, a netip.Addr) (netip.Prefix, bool) {
	best, found := netip.Prefix{}, false
	for _, p := range prefixes {
		if !p.Contains(a) {
			continue
		}
		if !found || p.Bits() > best.Bits() {
			best, found = p, true
		}
	}
	return best, found
}

func TestLookupBasic(t *testing.T) {
	var b Builder
	b.AddPrefixString("10.0.0.0/8")
	b.AddPrefixString("10.1.2.0/24")
	b.AddPrefixString("192.168.0.0/16")
	tbl, err := b.BuildTable()
	if err != nil {
		t.Fatal(err)
	}

	// The nested case is the whole reason Lookup returns the longest: these two
	// prefixes merged into a single span, so the span alone cannot answer it.
	for _, tc := range []struct{ addr, want string }{
		{"10.1.2.5", "10.1.2.0/24"},
		{"10.1.3.5", "10.0.0.0/8"},
		{"10.255.255.255", "10.0.0.0/8"},
		{"192.168.1.1", "192.168.0.0/16"},
	} {
		got, ok := tbl.LookupString(tc.addr)
		if !ok {
			t.Errorf("%s: not found", tc.addr)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("%s: got %s want %s", tc.addr, got, tc.want)
		}
	}

	if _, ok := tbl.LookupString("11.0.0.1"); ok {
		t.Error("11.0.0.1 should not be found")
	}
	if _, ok := tbl.LookupString("not-an-address"); ok {
		t.Error("unparseable address should not be found")
	}
	var nilTbl *Table
	if _, ok := nilTbl.LookupString("10.0.0.1"); ok {
		t.Error("nil table should find nothing")
	}
}

// TestLookupAgreesWithLinearScan is the real test. A sweep that mis-orders its
// events, or loses a prefix from the active set, still returns *a* containing
// prefix for most addresses — just not the longest, and not everywhere. Only a
// comparison against an exhaustive scan catches that; one such ordering bug is
// what this test found.
func TestLookupAgreesWithLinearScan(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		v4   bool
	}{
		{"v4/small", 20, true},
		{"v4/medium", 500, true},
		{"v4/large", 20000, true},
		{"v6/medium", 500, false},
		{"v6/large", 20000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(tc.n) + 77))
			prefixes := randomPrefixes(rng, tc.n, tc.v4)

			var b Builder
			for _, p := range prefixes {
				b.AddPrefix(p)
			}
			tbl, err := b.BuildTable()
			if err != nil {
				t.Fatal(err)
			}

			hits, misses := 0, 0
			for i := 0; i < 20000; i++ {
				a := sampleAddr(rng, prefixes, tc.v4)
				want, wantOK := longestLinear(prefixes, a)
				got, gotOK := tbl.Lookup(a)

				if gotOK != wantOK {
					t.Fatalf("%s: found=%v want %v", a, gotOK, wantOK)
				}
				if wantOK {
					hits++
					// Several prefixes may share the longest length and all
					// contain the address; any of them is a correct answer.
					if got.Bits() != want.Bits() || !got.Contains(a) {
						t.Fatalf("%s: got %s want a /%d containing it", a, got, want.Bits())
					}
				} else {
					misses++
				}

				// Contains must agree with Lookup, since they share the index.
				if tbl.Contains(a) != wantOK {
					t.Fatalf("%s: Contains disagrees with Lookup", a)
				}
			}
			if hits == 0 || misses == 0 {
				t.Fatalf("degenerate sample: %d hits, %d misses", hits, misses)
			}
			v4, v6 := tbl.Spans()
			g4, g6 := tbl.WorstScan()
			t.Logf("%d prefixes -> %d/%d spans, worst scan %d/%d, %d hits %d misses",
				len(prefixes), v4, v6, g4, g6, hits, misses)
		})
	}
}

// TestTableAndSetAgree pins that the two structures decide membership
// identically: Table adds an answer, it does not change the question.
func TestTableAndSetAgree(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	prefixes := randomPrefixes(rng, 2000, true)

	var b Builder
	for _, p := range prefixes {
		b.AddPrefix(p)
	}
	set, err := b.BuildSet()
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := b.BuildTable()
	if err != nil {
		t.Fatal(err)
	}

	// The counts differ by design and the direction is the point: a Set merges
	// everything touching, a Table must also cut wherever the winning prefix
	// changes, so a Table never has fewer intervals.
	s4, _ := set.Spans()
	t4, _ := tbl.Spans()
	if t4 < s4 {
		t.Errorf("table has fewer intervals (%d) than the set has spans (%d)", t4, s4)
	}
	t.Logf("set %d spans, table %d intervals", s4, t4)
	for i := 0; i < 20000; i++ {
		a := sampleAddr(rng, prefixes, true)
		if set.Contains(a) != tbl.Contains(a) {
			t.Fatalf("%s: set and table disagree", a)
		}
	}
}

func TestBuildTableEmpty(t *testing.T) {
	var b Builder
	tbl, err := b.BuildTable()
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Contains(netip.MustParseAddr("1.2.3.4")) {
		t.Error("empty table should contain nothing")
	}
	if _, ok := tbl.Lookup(netip.MustParseAddr("1.2.3.4")); ok {
		t.Error("empty table should find nothing")
	}
	v4, v6 := tbl.WorstScan()
	if v4 != 0 || v6 != 0 {
		t.Errorf("empty table should have no intervals, got %d/%d", v4, v6)
	}
}

// TestLookupV6Nested covers the same nesting case in IPv6, where the span
// arithmetic crosses the word boundary.
func TestLookupV6Nested(t *testing.T) {
	var b Builder
	b.AddPrefixString("2620::/32")
	b.AddPrefixString("2620:0:1::/48")
	b.AddPrefixString("2620:0:1:2::/64")
	tbl, err := b.BuildTable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ addr, want string }{
		{"2620:0:1:2::5", "2620:0:1:2::/64"},
		{"2620:0:1:3::5", "2620:0:1::/48"},
		{"2620:0:2::5", "2620::/32"},
	} {
		got, ok := tbl.LookupString(tc.addr)
		if !ok {
			t.Errorf("%s: not found", tc.addr)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("%s: got %s want %s", tc.addr, got, tc.want)
		}
	}
}
