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

// A Table is a Set that also remembers prefixes, so the two must answer
// membership identically — including for an AddRange, which covers addresses
// but has no prefix to name. Contains once derived its answer from Lookup and
// so reported false for exactly those addresses, and no test caught it because
// TestTableAndSetAgree feeds the Builder nothing but prefixes.
func TestTableContainsCoversRanges(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lo, hi     string
		prefix     string // also covers the range's middle, or "" for none
		mid        string
		below      string
		above      string
		wantPrefix string
	}{
		{
			name:  "v4 range only",
			lo:    "10.0.0.5",
			hi:    "10.0.0.9",
			mid:   "10.0.0.7",
			below: "10.0.0.4",
			above: "10.0.0.10",
		},
		{
			name:       "v4 range under a prefix",
			lo:         "10.0.0.5",
			hi:         "10.0.0.9",
			prefix:     "10.0.0.0/24",
			mid:        "10.0.0.7",
			below:      "9.255.255.255",
			above:      "10.0.1.0",
			wantPrefix: "10.0.0.0/24",
		},
		{
			name:  "v6 range only",
			lo:    "2001:db8::5",
			hi:    "2001:db8::9",
			mid:   "2001:db8::7",
			below: "2001:db8::4",
			above: "2001:db8::a",
		},
		{
			name:       "v6 range under a prefix",
			lo:         "2001:db8::5",
			hi:         "2001:db8::9",
			prefix:     "2001:db8::/64",
			mid:        "2001:db8::7",
			below:      "2001:db7:ffff:ffff:ffff:ffff:ffff:ffff",
			above:      "2001:db8:0:1::",
			wantPrefix: "2001:db8::/64",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b Builder
			b.AddRange(netip.MustParseAddr(tc.lo), netip.MustParseAddr(tc.hi))
			if tc.prefix != "" {
				b.AddPrefixString(tc.prefix)
			}
			set, err := b.BuildSet()
			if err != nil {
				t.Fatal(err)
			}
			tbl, err := b.BuildTable()
			if err != nil {
				t.Fatal(err)
			}

			for _, in := range []string{tc.lo, tc.mid, tc.hi} {
				a := netip.MustParseAddr(in)
				if !tbl.Contains(a) {
					t.Errorf("%s: table should contain it", in)
				}
				if set.Contains(a) != tbl.Contains(a) {
					t.Errorf("%s: set and table disagree", in)
				}
			}
			for _, out := range []string{tc.below, tc.above} {
				a := netip.MustParseAddr(out)
				if tbl.Contains(a) {
					t.Errorf("%s: table should not contain it", out)
				}
				if set.Contains(a) != tbl.Contains(a) {
					t.Errorf("%s: set and table disagree", out)
				}
			}

			// Lookup stays the narrower question: it reports a prefix or
			// nothing, and a range is not a prefix.
			got, ok := tbl.LookupString(tc.mid)
			if tc.wantPrefix == "" {
				if ok {
					t.Errorf("%s: range has no prefix to report, got %s", tc.mid, got)
				}
			} else if !ok || got.String() != tc.wantPrefix {
				t.Errorf("%s: got %s,%v want %s", tc.mid, got, ok, tc.wantPrefix)
			}
		})
	}
}

// TestTableAndSetAgree with ranges mixed in, which is the shape that exposed
// the divergence.
func TestTableAndSetAgreeWithRanges(t *testing.T) {
	for _, v4 := range []bool{true, false} {
		rng := rand.New(rand.NewSource(1721))
		prefixes := randomPrefixes(rng, 500, v4)

		var b Builder
		for _, p := range prefixes[:250] {
			b.AddPrefix(p)
		}
		// The rest become ranges: same covered addresses, no prefix recorded.
		for _, p := range prefixes[250:] {
			b.AddRange(p.Addr(), lastOf(p))
		}
		set, err := b.BuildSet()
		if err != nil {
			t.Fatal(err)
		}
		tbl, err := b.BuildTable()
		if err != nil {
			t.Fatal(err)
		}

		for i := 0; i < 20000; i++ {
			a := sampleAddr(rng, prefixes, v4)
			if set.Contains(a) != tbl.Contains(a) {
				t.Fatalf("v4=%v %s: set says %v, table says %v",
					v4, a, set.Contains(a), tbl.Contains(a))
			}
			// Whatever Lookup does report must be a genuine cover.
			if p, ok := tbl.Lookup(a); ok && !p.Contains(a) {
				t.Fatalf("v4=%v %s: Lookup returned %s, which does not contain it", v4, a, p)
			}
		}
	}
}
