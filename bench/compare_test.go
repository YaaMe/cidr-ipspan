package bench

import (
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"runtime"
	"testing"

	"github.com/YaaMe/cidr-ipspan"
	cidrange "github.com/YaaMe/cidrange-go"
	"github.com/gaissmai/bart"
	"github.com/yl2chen/cidranger"
	"go4.org/netipx"
)

// The comparison runs against eight providers' published ranges rather than one
// snapshot, because the providers differ enough that a structure can win on one
// and lose on another: after merging, Linode's 5409 IPv4 prefixes are 95 spans
// and GitHub's 5745 are 2017.

func prefixesOf(nets []*net.IPNet) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(nets))
	for _, n := range nets {
		if p, err := netip.ParsePrefix(n.String()); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

func buildSet(nets []*net.IPNet) *ipspan.Set {
	var b ipspan.Builder
	for _, p := range prefixesOf(nets) {
		b.AddPrefix(p)
	}
	s, err := b.BuildSet()
	if err != nil {
		panic(err)
	}
	return s
}

func buildTable(nets []*net.IPNet) *ipspan.Table {
	var b ipspan.Builder
	for _, p := range prefixesOf(nets) {
		b.AddPrefix(p)
	}
	t, err := b.BuildTable()
	if err != nil {
		panic(err)
	}
	return t
}

// For the membership comparison, bart.Lite is the right opponent: it stores no
// payload, which is the same thing ipspan.Set does. Timing ipspan.Set against a
// bart.Table[netip.Prefix] would have made bart carry the cost of an ability it
// was not being asked to use.
func buildBartLite(nets []*net.IPNet) *bart.Lite {
	t := new(bart.Lite)
	for _, p := range prefixesOf(nets) {
		t.Insert(p)
	}
	return t
}

// For the longest-prefix-match comparison bart holds the prefix as its value,
// so its Lookup answers the same question ipspan.Table.Lookup does.
func buildBart(nets []*net.IPNet) *bart.Table[netip.Prefix] {
	t := new(bart.Table[netip.Prefix])
	for _, p := range prefixesOf(nets) {
		t.Insert(p, p)
	}
	return t
}

// bart.Fast carries a payload like bart.Table but trades memory for speed, so
// it is the faster opponent for the longest-prefix-match comparison. Suggested
// by bart's author in YaaMe/cidrange-go#6.
func buildBartFast(nets []*net.IPNet) *bart.Fast[netip.Prefix] {
	t := new(bart.Fast[netip.Prefix])
	for _, p := range prefixesOf(nets) {
		t.Insert(p, p)
	}
	return t
}

// cidrange is the sibling repo this package grew out of. It is measured here
// rather than in its own module so that both structures face the same probes,
// the same corpora and the same bart instances in one process — the separate
// harnesses were not comparable, since they configured bart differently.
func buildCidrange(nets []*net.IPNet) *cidrange.IPRanger {
	r := cidrange.NewIPRanger()
	for _, n := range nets {
		if err := r.InsertCIDR(n); err != nil {
			panic(err)
		}
	}
	r.GenTree(0, 0) // adaptive bucket count
	return r
}

func buildCidranger(nets []*net.IPNet) cidranger.Ranger {
	r := cidranger.NewPCTrieRanger()
	for _, n := range nets {
		if err := r.Insert(cidranger.NewBasicRangerEntry(*n)); err != nil {
			panic(err)
		}
	}
	return r
}

func buildNetipx(nets []*net.IPNet) *netipx.IPSet {
	var b netipx.IPSetBuilder
	for _, p := range prefixesOf(nets) {
		b.AddPrefix(p)
	}
	s, err := b.IPSet()
	if err != nil {
		panic(err)
	}
	return s
}

// probes returns rotating addresses, a given share inside the corpus. A single
// fixed probe pins one cache line and makes every branch predictable, which
// measures a best case no caller has.
func probes(nets []*net.IPNet, hitRatio float64, v4 bool, n int) ([]net.IP, []netip.Addr) {
	rng := rand.New(rand.NewSource(99))
	ips := make([]net.IP, 0, n)
	addrs := make([]netip.Addr, 0, n)
	for i := 0; i < n; i++ {
		var ip net.IP
		if rng.Float64() < hitRatio && len(nets) > 0 {
			x := nets[rng.Intn(len(nets))]
			ip = append(net.IP(nil), x.IP...)
			ones, bits := x.Mask.Size()
			for b := ones; b < bits; b++ {
				if rng.Intn(2) == 1 {
					ip[b/8] |= 1 << (7 - uint(b%8))
				}
			}
		} else if v4 {
			ip = net.IPv4(240, byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))).To4()
		} else {
			ip = make(net.IP, net.IPv6len)
			copy(ip, net.ParseIP("2001:db8::"))
			for j := 4; j < net.IPv6len; j++ {
				ip[j] = byte(rng.Intn(256))
			}
		}
		ips = append(ips, ip)
		a, _ := netip.AddrFromSlice(ip)
		addrs = append(addrs, a.Unmap())
	}
	return ips, addrs
}

// benchProviders are three shapes: large and heavily collapsing, fragmented,
// and collapsing almost completely. All eight are checked for correctness.
var benchProviders = map[string]bool{"aws": true, "github": true, "linode": true}

// TestAgree guards the comparison. A structure answering wrongly could look
// fast for the wrong reason, and Table must additionally agree on *which*
// prefix, not merely that there is one.
func TestAgree(t *testing.T) {
	corpora, err := Load("")
	if err != nil {
		t.Skipf("no corpora: %v (run ./fetch.sh)", err)
	}
	for _, c := range corpora {
		all := append(append([]*net.IPNet{}, c.V4...), c.V6...)
		set, tbl := buildSet(all), buildTable(all)
		lite, fast := buildBartLite(all), buildBartFast(all)
		cr := buildCidrange(all)
		bt, cg, nx := buildBart(all), buildCidranger(all), buildNetipx(all)
		pfxs := prefixesOf(all)

		ips, addrs := probes(all, 0.5, true, 4000)
		for i := range ips {
			// Membership, against an exhaustive scan.
			want := false
			for _, p := range pfxs {
				if p.Contains(addrs[i]) {
					want = true
					break
				}
			}
			if got := set.Contains(addrs[i]); got != want {
				t.Fatalf("%s: ipspan.Set(%s)=%v want %v", c.Name, addrs[i], got, want)
			}
			if got := tbl.Contains(addrs[i]); got != want {
				t.Fatalf("%s: ipspan.Table(%s)=%v want %v", c.Name, addrs[i], got, want)
			}
			if got := bt.Contains(addrs[i]); got != want {
				t.Fatalf("%s: bart(%s)=%v want %v", c.Name, addrs[i], got, want)
			}
			if got := lite.Contains(addrs[i]); got != want {
				t.Fatalf("%s: bart.Lite(%s)=%v want %v", c.Name, addrs[i], got, want)
			}
			if got := fast.Contains(addrs[i]); got != want {
				t.Fatalf("%s: bart.Fast(%s)=%v want %v", c.Name, addrs[i], got, want)
			}
			// OverlapContains, not Contains: provider lists do overlap, and the
			// cheaper entry point assumes they do not.
			if got := cr.OverlapContains(ips[i]); got != want {
				t.Fatalf("%s: cidrange(%s)=%v want %v", c.Name, ips[i], got, want)
			}
			if got, _ := cg.Contains(ips[i]); got != want {
				t.Fatalf("%s: cidranger(%s)=%v want %v", c.Name, ips[i], got, want)
			}
			if got := nx.Contains(addrs[i]); got != want {
				t.Fatalf("%s: netipx(%s)=%v want %v", c.Name, addrs[i], got, want)
			}

			// Longest-prefix match: ipspan.Table against bart, which has the
			// prefix as its value.
			gotP, gotOK := tbl.Lookup(addrs[i])
			wantP, wantOK := bt.Lookup(addrs[i])
			if gotOK != wantOK {
				t.Fatalf("%s: Lookup(%s) found=%v, bart=%v", c.Name, addrs[i], gotOK, wantOK)
			}
			if wantOK && gotP.Bits() != wantP.Bits() {
				t.Fatalf("%s: Lookup(%s)=%s, bart=%s", c.Name, addrs[i], gotP, wantP)
			}
			fastP, fastOK := fast.Lookup(addrs[i])
			if fastOK != wantOK || (wantOK && fastP.Bits() != wantP.Bits()) {
				t.Fatalf("%s: bart.Fast Lookup(%s)=%s/%v, bart.Table=%s/%v",
					c.Name, addrs[i], fastP, fastOK, wantP, wantOK)
			}
		}
		t.Logf("%-14s %d blocks, %d probes: membership and LPM agree everywhere",
			c.Name, len(all), len(ips))
	}
}

// BenchmarkBuild reports what each structure costs to construct and to hold.
//
// Retained size is not something Go's allocation counters can report — B/op
// measures churn during the build, not what survives it — so it is measured
// with MemStats and surfaced as a custom metric. All eight corpora are covered
// rather than the three the lookup benchmarks use, because memory does not take
// long to measure and the providers differ more in shape than in size.
func BenchmarkBuild(b *testing.B) {
	corpora, err := Load("")
	if err != nil {
		b.Skipf("no corpora: %v", err)
	}
	for _, c := range corpora {
		all := append(append([]*net.IPNet{}, c.V4...), c.V6...)
		for _, im := range []struct {
			name  string
			build func() any
		}{
			{"ipspanSet", func() any { return buildSet(all) }},
			{"ipspanTable", func() any { return buildTable(all) }},
			{"bartLite", func() any { return buildBartLite(all) }},
			{"bartTable", func() any { return buildBart(all) }},
			{"bartFast", func() any { return buildBartFast(all) }},
			{"cidrange", func() any { return buildCidrange(all) }},
			{"netipx", func() any { return buildNetipx(all) }},
			{"cidranger", func() any { return buildCidranger(all) }},
		} {
			im := im
			b.Run(c.Name+"/"+im.name, func(b *testing.B) {
				retained := retainedBytes(im.build, all)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					sinkAny = im.build()
				}
				b.StopTimer()
				// After the loop, not before: ResetTimer clears the custom
				// metric map, so anything reported earlier is silently dropped.
				b.ReportMetric(retained/float64(len(all)), "B/block")
				b.ReportMetric(retained/(1<<20), "MB-held")
			})
		}
	}
}

var sinkAny any

// retainedBytes measures what a structure holds after construction, as opposed
// to what it allocated on the way.
//
// The input is kept reachable on purpose: every structure here copies it, so
// without that the collector would reclaim the source between the two samples
// and the freed memory would cancel out the structure's own — which flatters
// precisely the implementations that are careful about ownership.
func retainedBytes(build func() any, keep any) float64 {
	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	h := build()
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	n := float64(int64(after.HeapAlloc) - int64(before.HeapAlloc))
	runtime.KeepAlive(h)
	runtime.KeepAlive(keep)
	return n
}

func TestFootprint(t *testing.T) {
	corpora, err := Load("")
	if err != nil {
		t.Skipf("no corpora: %v", err)
	}
	t.Logf("%-14s %-16s %10s %12s", "provider", "impl", "MB", "bytes/block")
	for _, c := range corpora {
		all := append(append([]*net.IPNet{}, c.V4...), c.V6...)
		if len(all) < 1000 {
			continue
		}
		measure := func(name string, build func() any) {
			runtime.GC()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			h := build()
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&after)
			b := float64(int64(after.HeapAlloc) - int64(before.HeapAlloc))
			t.Logf("%-14s %-16s %10.2f %12.0f", c.Name, name, b/(1<<20), b/float64(len(all)))
			// Inputs stay reachable: every structure here copies its input, and
			// without this the GC would reclaim the source between the samples
			// and the freed memory would cancel out the structure's own.
			runtime.KeepAlive(h)
			runtime.KeepAlive(all)
		}
		measure("ipspan.Set", func() any { return buildSet(all) })
		measure("ipspan.Table", func() any { return buildTable(all) })
		measure("bart.Lite", func() any { return buildBartLite(all) })
		measure("bart.Table", func() any { return buildBart(all) })
		measure("bart.Fast", func() any { return buildBartFast(all) })
		measure("cidrange", func() any { return buildCidrange(all) })
		measure("cidranger", func() any { return buildCidranger(all) })
		measure("netipx", func() any { return buildNetipx(all) })
	}
}

func BenchmarkMembership(b *testing.B) {
	corpora, err := Load("")
	if err != nil {
		b.Skipf("no corpora: %v", err)
	}
	for _, c := range corpora {
		if !benchProviders[c.Name] {
			continue
		}
		all := append(append([]*net.IPNet{}, c.V4...), c.V6...)
		set, tbl := buildSet(all), buildTable(all)
		lite, fast := buildBartLite(all), buildBartFast(all)
		cr := buildCidrange(all)
		bt, cg, nx := buildBart(all), buildCidranger(all), buildNetipx(all)
		_ = bt

		// Both families: the two index paths behave differently enough that
		// probing only IPv4 hides half the picture.
		for _, fam := range []struct {
			label string
			nets  []*net.IPNet
			v4    bool
		}{{"v4", c.V4, true}, {"v6", c.V6, false}} {
			for _, mix := range []struct {
				name  string
				ratio float64
			}{{"AllHit", 1.0}, {"HalfHit", 0.5}, {"AllMiss", 0.0}} {
				if len(fam.nets) == 0 {
					continue
				}
				ips, addrs := probes(fam.nets, mix.ratio, fam.v4, 8192)
				pre := fmt.Sprintf("%s/%s/%s/", c.Name, fam.label, mix.name)

				b.Run(pre+"ipspanSet", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						set.Contains(addrs[i&8191])
					}
				})
				b.Run(pre+"ipspanTable", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						tbl.Contains(addrs[i&8191])
					}
				})
				b.Run(pre+"bartLite", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						lite.Contains(addrs[i&8191])
					}
				})
				b.Run(pre+"bartTable", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						bt.Contains(addrs[i&8191])
					}
				})
				b.Run(pre+"bartFast", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						fast.Contains(addrs[i&8191])
					}
				})
				b.Run(pre+"cidrange", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						cr.OverlapContains(ips[i&8191])
					}
				})
				b.Run(pre+"cidranger", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						cg.Contains(ips[i&8191])
					}
				})
				b.Run(pre+"netipx", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						nx.Contains(addrs[i&8191])
					}
				})
			}
		}
	}
}

// BenchmarkLookup compares returning the matching prefix, which only two of
// these can do.
func BenchmarkLookup(b *testing.B) {
	corpora, err := Load("")
	if err != nil {
		b.Skipf("no corpora: %v", err)
	}
	for _, c := range corpora {
		if !benchProviders[c.Name] {
			continue
		}
		all := append(append([]*net.IPNet{}, c.V4...), c.V6...)
		tbl, bt, fast := buildTable(all), buildBart(all), buildBartFast(all)

		for _, fam := range []struct {
			label string
			nets  []*net.IPNet
			v4    bool
		}{{"v4", c.V4, true}, {"v6", c.V6, false}} {
			for _, mix := range []struct {
				name  string
				ratio float64
			}{{"AllHit", 1.0}, {"HalfHit", 0.5}, {"AllMiss", 0.0}} {
				if len(fam.nets) == 0 {
					continue
				}
				_, addrs := probes(fam.nets, mix.ratio, fam.v4, 8192)
				pre := fmt.Sprintf("%s/%s/%s/", c.Name, fam.label, mix.name)

				b.Run(pre+"ipspanTable", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						tbl.Lookup(addrs[i&8191])
					}
				})
				b.Run(pre+"bart", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						bt.Lookup(addrs[i&8191])
					}
				})
				b.Run(pre+"bartFast", func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						fast.Lookup(addrs[i&8191])
					}
				})
			}
		}
	}
}
