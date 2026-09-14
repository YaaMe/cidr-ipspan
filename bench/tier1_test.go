package bench

// The routing table, as opposed to the provider allocations everything else
// here measures.
//
// Two reasons it is worth its 2.8 MB in the repository. It is fixed and
// public, so figures from it are reproducible in a way the fetched provider
// lists and a licensed commercial database are not. And it is the shape least
// favourable to this package: provider allocations barely nest, while a
// routing table is announcements inside announcements, which is the case bart
// exists for.

import (
	"bufio"
	"compress/gzip"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"testing"

	ipspan "github.com/YaaMe/cidr-ipspan"
	"github.com/gaissmai/bart"
)

func loadTier1(tb testing.TB) (v4, v6 []netip.Prefix) {
	tb.Helper()
	fh, err := os.Open("testdata/tier1-routes.txt.gz")
	if err != nil {
		tb.Skipf("no tier1 corpus: %v", err)
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		tb.Fatal(err)
	}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		p, err := netip.ParsePrefix(strings.TrimSpace(sc.Text()))
		if err != nil {
			continue
		}
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	return v4, v6
}

// TestTier1Shape records what the corpus does to each structure's size, which
// is the question the design turns on: membership discards the prefixes and
// collapses, longest-prefix match keeps them and does not.
func TestTier1Shape(t *testing.T) {
	v4, v6 := loadTier1(t)

	var b ipspan.Builder
	for _, p := range v4 {
		b.AddPrefix(p)
	}
	for _, p := range v6 {
		b.AddPrefix(p)
	}
	set, err := b.BuildSet()
	if err != nil {
		t.Fatal(err)
	}
	s4, s6 := set.Spans()

	t.Logf("IPv4 prefixes %d -> %d spans (%.1f%% collapsed)",
		len(v4), s4, 100*(1-float64(s4)/float64(len(v4))))
	t.Logf("IPv6 prefixes %d -> %d spans (%.1f%% collapsed)",
		len(v6), s6, 100*(1-float64(s6)/float64(len(v6))))

	// The collapse is not a property of provider lists. If this ever stops
	// holding on a routing table, the premise of the package has moved.
	if s4 >= len(v4)/2 {
		t.Errorf("IPv4 barely collapsed: %d prefixes -> %d spans", len(v4), s4)
	}
}

// TestTier1Agrees is the correctness gate before any timing is believed.
func TestTier1Agrees(t *testing.T) {
	v4, _ := loadTier1(t)

	var b ipspan.Builder
	for _, p := range v4 {
		b.AddPrefix(p)
	}
	set, err := b.BuildSet()
	if err != nil {
		t.Fatal(err)
	}
	lite := new(bart.Lite)
	for _, p := range v4 {
		lite.Insert(p)
	}

	_, addrs := probes(toIPNets(v4), 0.5, true, 4000)
	for _, a := range addrs {
		if set.Contains(a) != lite.Contains(a) {
			t.Fatalf("disagree at %s", a)
		}
	}
	t.Logf("%d probes: ipspan.Set and bart.Lite agree", len(addrs))
}

func toIPNets(ps []netip.Prefix) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(ps))
	for _, p := range ps {
		_, n, err := net.ParseCIDR(p.String())
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

// BenchmarkTier1 reports retained size alongside lookup cost.
//
// Each structure is built in its own sub-benchmark but they share a process,
// so the retained figures here are indicative. The authoritative ones come
// from building one structure per process — mixing them lets the garbage of
// an earlier build be collected during a later measurement, which once
// produced a negative reading and is how that contamination announces itself.
func BenchmarkTier1(b *testing.B) {
	v4, _ := loadTier1(b)

	impls := []struct {
		name  string
		build func() (any, func(netip.Addr) bool)
	}{
		{"ipspan.Set", func() (any, func(netip.Addr) bool) {
			var bld ipspan.Builder
			for _, p := range v4 {
				bld.AddPrefix(p)
			}
			s, _ := bld.BuildSet()
			return s, s.Contains
		}},
		{"bart.Lite", func() (any, func(netip.Addr) bool) {
			t := new(bart.Lite)
			for _, p := range v4 {
				t.Insert(p)
			}
			return t, t.Contains
		}},
		{"bart.Fast", func() (any, func(netip.Addr) bool) {
			t := new(bart.Fast[struct{}])
			for _, p := range v4 {
				t.Insert(p, struct{}{})
			}
			return t, t.Contains
		}},
	}

	_, addrs := probes(toIPNets(v4), 0.5, true, 8192)

	for _, im := range impls {
		b.Run(im.name, func(b *testing.B) {
			runtime.GC()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			h, lookup := im.build()
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&after)
			retained := float64(int64(after.HeapAlloc) - int64(before.HeapAlloc))

			b.ResetTimer()
			var sink bool
			for i := 0; i < b.N; i++ {
				sink = lookup(addrs[i&8191])
			}
			b.StopTimer()

			b.ReportMetric(retained/float64(len(v4)), "B/prefix")
			b.ReportMetric(retained/(1<<20), "MB")
			runtime.KeepAlive(h)
			runtime.KeepAlive(v4)
			_ = sink
		})
	}
}
