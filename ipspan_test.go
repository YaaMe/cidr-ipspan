package ipspan

import (
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"testing"
)

func mustBuild(t *testing.T, cidrs ...string) *Set {
	t.Helper()
	var b Builder
	for _, c := range cidrs {
		b.AddPrefixString(c)
	}
	s, err := b.BuildSet()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return s
}

// linearContains is the definition this package optimises: does any block
// contain the address. Every correctness test below compares against it rather
// than against hand-written expectations, because the merge and the index can
// each be wrong in ways that look plausible.
func linearContains(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func TestBasic(t *testing.T) {
	s := mustBuild(t, "192.168.1.0/24", "10.0.0.0/8", "2620::/32")

	for _, in := range []string{"192.168.1.0", "192.168.1.255", "10.0.0.1",
		"10.255.255.255", "2620::1", "2620:0:ffff:ffff:ffff:ffff:ffff:ffff"} {
		if !s.ContainsString(in) {
			t.Errorf("%s should be in the set", in)
		}
	}
	for _, out := range []string{"192.168.2.0", "192.168.0.255", "9.255.255.255",
		"11.0.0.0", "2620:1::", "2621::1", "261f::1"} {
		if s.ContainsString(out) {
			t.Errorf("%s should not be in the set", out)
		}
	}
}

// TestBoundaries pins the prefix lengths where the mask arithmetic changes
// shape: /0 shifts by the whole width, /64 sits on the word boundary, /65 is
// the first to reach the low word, and /32 and /128 are exact hosts.
func TestBoundaries(t *testing.T) {
	cases := []struct {
		cidr string
		in   []string
		out  []string
	}{
		{"0.0.0.0/0", []string{"0.0.0.0", "8.8.8.8", "255.255.255.255"}, nil},
		{"::/0", []string{"::", "2620::1", "ffff::ffff"}, nil},
		{"1.2.3.4/32", []string{"1.2.3.4"}, []string{"1.2.3.3", "1.2.3.5"}},
		{"255.255.255.255/32", []string{"255.255.255.255"}, []string{"255.255.255.254"}},
		{"2620:107:300f::/64",
			[]string{"2620:107:300f::", "2620:107:300f::ffff:ffff:ffff:ffff"},
			[]string{"2620:107:300e:ffff::", "2620:107:3010::"}},
		{"2620:107:300f:0:8000::/65",
			[]string{"2620:107:300f:0:8000::", "2620:107:300f:0:ffff:ffff:ffff:ffff"},
			[]string{"2620:107:300f:0:7fff:ffff:ffff:ffff"}},
		{"2620::1/128", []string{"2620::1"}, []string{"2620::", "2620::2"}},
		{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff/128",
			[]string{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"},
			[]string{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe"}},
	}
	for _, tc := range cases {
		t.Run(tc.cidr, func(t *testing.T) {
			s := mustBuild(t, tc.cidr)
			p := netip.MustParsePrefix(tc.cidr)
			for _, in := range tc.in {
				a := netip.MustParseAddr(in)
				if !s.Contains(a) {
					t.Errorf("%s should contain %s", tc.cidr, in)
				}
				if !p.Contains(a) {
					t.Errorf("stdlib disagrees: %s should contain %s", tc.cidr, in)
				}
			}
			for _, out := range tc.out {
				a := netip.MustParseAddr(out)
				if s.Contains(a) {
					t.Errorf("%s should not contain %s", tc.cidr, out)
				}
				if p.Contains(a) {
					t.Errorf("stdlib disagrees: %s should not contain %s", tc.cidr, out)
				}
			}
		})
	}
}

// TestMergeCollapses checks the property the whole design rests on. The
// resulting span count is observable, so a merge that silently failed to join
// adjacent blocks would show up here rather than only as lost speed.
func TestMergeCollapses(t *testing.T) {
	cases := []struct {
		name  string
		cidrs []string
		spans int
	}{
		{"single", []string{"10.0.0.0/8"}, 1},
		{"exact duplicate", []string{"10.0.0.0/8", "10.0.0.0/8"}, 1},
		{"nested", []string{"10.0.0.0/8", "10.1.2.0/24"}, 1},
		{"nested reversed order", []string{"10.1.2.0/24", "10.0.0.0/8"}, 1},
		{"adjacent halves", []string{"10.0.0.0/9", "10.128.0.0/9"}, 1},
		{"adjacent across octet", []string{"10.0.0.0/8", "11.0.0.0/8"}, 1},
		{"gap", []string{"10.0.0.0/8", "12.0.0.0/8"}, 2},
		{"three into one", []string{"10.0.0.0/10", "10.64.0.0/10", "10.128.0.0/9"}, 1},
		{"overlapping", []string{"10.0.0.0/9", "10.64.0.0/10"}, 1},
		{"whole space absorbs", []string{"0.0.0.0/0", "10.0.0.0/8", "200.0.0.0/8"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustBuild(t, tc.cidrs...)
			v4, _ := s.Spans()
			if v4 != tc.spans {
				t.Errorf("got %d spans, want %d", v4, tc.spans)
			}
		})
	}
}

func TestMergeCollapsesV6(t *testing.T) {
	s := mustBuild(t, "2620::/33", "2620:0:8000::/33")
	if _, v6 := s.Spans(); v6 != 1 {
		t.Errorf("adjacent v6 halves should merge, got %d spans", v6)
	}
	s = mustBuild(t, "2620::/32", "2622::/32")
	if _, v6 := s.Spans(); v6 != 2 {
		t.Errorf("separated v6 blocks should not merge, got %d spans", v6)
	}
}

// randomPrefixes generates a corpus with enough overlap and adjacency to
// exercise merging, and enough spread to exercise the index.
func randomPrefixes(rng *rand.Rand, n int, v4 bool) []netip.Prefix {
	out := make([]netip.Prefix, 0, n)
	for i := 0; i < n; i++ {
		if v4 {
			var b [4]byte
			rng.Read(b[:])
			b[0] = 1 + byte(rng.Intn(222))
			bits := 8 + rng.Intn(25)
			out = append(out, netip.PrefixFrom(netip.AddrFrom4(b), bits).Masked())
			continue
		}
		var b [16]byte
		rng.Read(b[:])
		b[0] = 0x20 | (b[0] & 0x1f)
		bits := 16 + rng.Intn(113)
		out = append(out, netip.PrefixFrom(netip.AddrFrom16(b), bits).Masked())
	}
	return out
}

// TestAgreesWithLinearScan is the test that matters. Merging and indexing can
// each fail in ways that still produce a plausible-looking set, so both are
// checked against an exhaustive scan over addresses drawn to land inside,
// beside and far from the corpus.
func TestAgreesWithLinearScan(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		v4   bool
	}{
		{"v4/small", 20, true},
		{"v4/medium", 500, true},
		{"v4/large", 20000, true},
		{"v6/small", 20, false},
		{"v6/medium", 500, false},
		{"v6/large", 20000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(tc.n) + 1))
			prefixes := randomPrefixes(rng, tc.n, tc.v4)

			var b Builder
			for _, p := range prefixes {
				b.AddPrefix(p)
			}
			s, err := b.BuildSet()
			if err != nil {
				t.Fatal(err)
			}

			hits, misses := 0, 0
			for i := 0; i < 20000; i++ {
				a := sampleAddr(rng, prefixes, tc.v4)
				want := linearContains(prefixes, a)
				if got := s.Contains(a); got != want {
					t.Fatalf("disagreement for %s: got %v want %v", a, got, want)
				}
				if want {
					hits++
				} else {
					misses++
				}
			}
			// A run that never hits or never misses would pass vacuously.
			if hits == 0 || misses == 0 {
				t.Fatalf("degenerate sample: %d hits, %d misses", hits, misses)
			}
			v4, v6 := s.Spans()
			ws4, ws6 := s.WorstScan()
			t.Logf("%d prefixes -> %d/%d spans, worst scan %d/%d, %d hits %d misses",
				len(prefixes), v4, v6, ws4, ws6, hits, misses)
		})
	}
}

// sampleAddr draws addresses that land inside a prefix, just outside one, or
// anywhere — so the boundaries of each span get probed, not just their middles.
func sampleAddr(rng *rand.Rand, prefixes []netip.Prefix, v4 bool) netip.Addr {
	p := prefixes[rng.Intn(len(prefixes))]
	switch rng.Intn(4) {
	case 0:
		return randomIn(rng, p)
	case 1:
		// One address either side of a span edge is where an off-by-one lives.
		a := p.Addr()
		if rng.Intn(2) == 0 {
			return a.Prev()
		}
		return lastOf(p).Next()
	case 2:
		return lastOf(p)
	default:
		if v4 {
			var b [4]byte
			rng.Read(b[:])
			return netip.AddrFrom4(b)
		}
		var b [16]byte
		rng.Read(b[:])
		return netip.AddrFrom16(b)
	}
}

func randomIn(rng *rand.Rand, p netip.Prefix) netip.Addr {
	if p.Addr().Is4() {
		v := beUint32(p.Addr().As4()) | (uint32(rng.Uint64()) & hostMask32(p.Bits()))
		return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	base := u128FromBytes(p.Addr().As16())
	m := hostMask128(p.Bits())
	v := base.or(u128{rng.Uint64() & m.hi, rng.Uint64() & m.lo})
	return addrFromU128(v)
}

func lastOf(p netip.Prefix) netip.Addr {
	if p.Addr().Is4() {
		v := beUint32(p.Addr().As4()) | hostMask32(p.Bits())
		return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	return addrFromU128(u128FromBytes(p.Addr().As16()).or(hostMask128(p.Bits())))
}

func addrFromU128(v u128) netip.Addr {
	var b [16]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(v.hi >> (56 - 8*i))
		b[8+i] = byte(v.lo >> (56 - 8*i))
	}
	return netip.AddrFrom16(b)
}

func TestEmptyAndInvalid(t *testing.T) {
	var b Builder
	s, err := b.BuildSet()
	if err != nil {
		t.Fatal(err)
	}
	if s.ContainsString("1.2.3.4") || s.ContainsString("2620::1") {
		t.Error("empty set should contain nothing")
	}
	if s.Contains(netip.Addr{}) {
		t.Error("invalid address should not be contained")
	}
	if s.ContainsString("not-an-address") {
		t.Error("unparseable address should not be contained")
	}
	if s.ContainsIP(nil) {
		t.Error("nil net.IP should not be contained")
	}

	var nilSet *Set
	if nilSet.Contains(netip.MustParseAddr("1.2.3.4")) {
		t.Error("nil set should contain nothing")
	}
}

func TestBuildReportsError(t *testing.T) {
	var b Builder
	b.AddPrefixString("10.0.0.0/8")
	b.AddPrefixString("not-a-prefix")
	b.AddPrefixString("192.168.0.0/16")

	s, err := b.BuildSet()
	if err == nil {
		t.Fatal("expected an error for the malformed prefix")
	}
	// The good blocks must still be present: a caller may reasonably choose to
	// skip bad lines in a feed rather than abandon the whole set.
	if !s.ContainsString("10.1.2.3") || !s.ContainsString("192.168.1.1") {
		t.Error("valid blocks should survive a malformed one")
	}
}

// TestV4MappedFormsAgree covers the several spellings of one IPv4 address.
func TestV4MappedFormsAgree(t *testing.T) {
	s := mustBuild(t, "192.168.1.0/24")
	for _, form := range []string{"192.168.1.7", "::ffff:192.168.1.7"} {
		if !s.ContainsString(form) {
			t.Errorf("%s should be in the set", form)
		}
	}
	if !s.ContainsIP(net.ParseIP("192.168.1.7")) {
		t.Error("net.IP form should be in the set")
	}
	if !s.ContainsIP(net.IP{192, 168, 1, 7}) {
		t.Error("4-byte net.IP should be in the set")
	}

	// And a v4-mapped prefix must denote the same addresses as the plain one.
	var b Builder
	b.AddPrefix(netip.MustParsePrefix("::ffff:10.0.0.0/104"))
	m, err := b.BuildSet()
	if err != nil {
		t.Fatal(err)
	}
	if !m.ContainsString("10.1.2.3") {
		t.Error("v4-mapped prefix should cover the IPv4 address it denotes")
	}
}

func TestAddRange(t *testing.T) {
	var b Builder
	b.AddRange(netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("10.0.0.9"))
	s, err := b.BuildSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"10.0.0.5", "10.0.0.7", "10.0.0.9"} {
		if !s.ContainsString(in) {
			t.Errorf("%s should be in the range", in)
		}
	}
	for _, out := range []string{"10.0.0.4", "10.0.0.10"} {
		if s.ContainsString(out) {
			t.Errorf("%s should not be in the range", out)
		}
	}

	for _, bad := range [][2]string{
		{"10.0.0.9", "10.0.0.5"}, // reversed
		{"10.0.0.1", "2620::1"},  // mixed families
	} {
		var bb Builder
		bb.AddRange(netip.MustParseAddr(bad[0]), netip.MustParseAddr(bad[1]))
		if _, err := bb.BuildSet(); err == nil {
			t.Errorf("AddRange(%s, %s) should have failed", bad[0], bad[1])
		}
	}
}

// The merge must copy out of the input's backing array, not return a window
// into it.
//
// Merging in place is tempting and correct in every answer it gives, which is
// why no correctness test catches it: the result has the length of the spans
// but the capacity of the blocks, so a Set pins the whole input for its
// lifetime. The overhead scales with the number of blocks, which means it is
// worst exactly where this package is meant to be used — a 901,899-prefix
// routing table collapsing to 67,888 spans retained 8.3 MB instead of 0.8.
func TestMergeDoesNotPinTheInput(t *testing.T) {
	// Many blocks, few spans: 4096 adjacent /24s are one span.
	var b Builder
	for i := 0; i < 4096; i++ {
		b.AddPrefixString(fmt.Sprintf("10.%d.%d.0/24", i/256, i%256))
	}
	s, err := b.BuildSet()
	if err != nil {
		t.Fatal(err)
	}

	v4, _ := s.Spans()
	if v4 != 1 {
		t.Fatalf("4096 adjacent /24s should be one span, got %d", v4)
	}
	if got := cap(s.v4.spans); got > 4 {
		t.Errorf("spans has capacity %d for %d span(s): the input array is still pinned", got, v4)
	}
}

func TestMergeDoesNotPinTheInputV6(t *testing.T) {
	var b Builder
	for i := 0; i < 1024; i++ {
		b.AddPrefixString(fmt.Sprintf("2001:db8:%x::/48", i))
	}
	s, err := b.BuildSet()
	if err != nil {
		t.Fatal(err)
	}

	_, v6 := s.Spans()
	if v6 != 1 {
		t.Fatalf("1024 adjacent /48s should be one span, got %d", v6)
	}
	if got := cap(s.v6.spans); got > 4 {
		t.Errorf("spans has capacity %d for %d span(s): the input array is still pinned", got, v6)
	}
}
