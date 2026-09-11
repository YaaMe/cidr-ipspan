package ipspan

import (
	"fmt"
	"math/rand"
	"net/netip"
	"runtime"
	"testing"
)

// Benchmarks rotate through 8192 addresses rather than repeating one. A single
// fixed probe pins one cache line and makes every branch predictable, which
// measures a best case no caller has; on the package this one was extracted
// from, the difference between the two was a factor of two.

const nProbes = 8192

// bgpShaped generates prefixes with a length distribution loosely resembling a
// provider list: mostly /24, a tail of shorter aggregates that nest over them.
func bgpShaped(rng *rand.Rand, n int) []netip.Prefix {
	out := make([]netip.Prefix, 0, n)
	for i := 0; i < n; i++ {
		var bits int
		switch r := rng.Intn(100); {
		case r < 55:
			bits = 24
		case r < 75:
			bits = 20 + rng.Intn(4)
		case r < 90:
			bits = 16 + rng.Intn(4)
		case r < 98:
			bits = 12 + rng.Intn(4)
		default:
			bits = 8 + rng.Intn(4)
		}
		var b [4]byte
		rng.Read(b[:])
		b[0] = 1 + byte(rng.Intn(222))
		out = append(out, netip.PrefixFrom(netip.AddrFrom4(b), bits).Masked())
	}
	return out
}

func benchProbes(rng *rand.Rand, prefixes []netip.Prefix, hitRatio float64) []netip.Addr {
	out := make([]netip.Addr, nProbes)
	for i := range out {
		if rng.Float64() < hitRatio {
			out[i] = randomIn(rng, prefixes[rng.Intn(len(prefixes))])
			continue
		}
		// Reserved space, absent from any generated corpus.
		out[i] = netip.AddrFrom4([4]byte{240, byte(rng.Intn(256)), byte(rng.Intn(256)), byte(rng.Intn(256))})
	}
	return out
}

var (
	sinkBool   bool
	sinkPrefix netip.Prefix
)

func BenchmarkLookup(b *testing.B) {
	for _, n := range []int{1000, 100000} {
		rng := rand.New(rand.NewSource(42))
		prefixes := bgpShaped(rng, n)

		var bld Builder
		for _, p := range prefixes {
			bld.AddPrefix(p)
		}
		set, err := bld.BuildSet()
		if err != nil {
			b.Fatal(err)
		}
		tbl, err := bld.BuildTable()
		if err != nil {
			b.Fatal(err)
		}

		for _, mix := range []struct {
			name  string
			ratio float64
		}{{"AllHit", 1.0}, {"HalfHit", 0.5}, {"AllMiss", 0.0}} {
			probes := benchProbes(rand.New(rand.NewSource(7)), prefixes, mix.ratio)
			pre := fmt.Sprintf("n=%d/%s/", n, mix.name)

			b.Run(pre+"Set", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					sinkBool = set.Contains(probes[i&(nProbes-1)])
				}
			})
			// Table asked only for membership: the same indexed read, so this
			// measures what remembering the prefix costs when you do not use it.
			b.Run(pre+"TableContains", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					sinkBool = tbl.Contains(probes[i&(nProbes-1)])
				}
			})
			b.Run(pre+"TableLookup", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					sinkPrefix, sinkBool = tbl.Lookup(probes[i&(nProbes-1)])
				}
			})
		}
	}
}

// TestFootprint is not a benchmark but belongs with them: the choice between
// Set and Table is as much about memory as speed.
//
// The prefixes are kept alive across the measurement on purpose. Both
// structures copy their input, so without that the GC would reclaim the source
// slice between the two samples and the freed memory would cancel out the
// structure's own.
func TestFootprint(t *testing.T) {
	t.Logf("%-8s %-6s %10s %12s %10s", "blocks", "kind", "MB", "bytes/block", "intervals")
	for _, n := range []int{1000, 10000, 100000} {
		rng := rand.New(rand.NewSource(42))
		prefixes := bgpShaped(rng, n)

		measure := func(kind string, build func() (int, any)) {
			runtime.GC()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			spans, h := build()
			runtime.GC()
			runtime.GC()
			runtime.ReadMemStats(&after)
			bytes := float64(int64(after.HeapAlloc) - int64(before.HeapAlloc))
			t.Logf("%-8d %-6s %10.2f %12.0f %10d",
				n, kind, bytes/(1<<20), bytes/float64(n), spans)
			runtime.KeepAlive(h)
			runtime.KeepAlive(prefixes)
		}

		measure("Set", func() (int, any) {
			var bld Builder
			for _, p := range prefixes {
				bld.AddPrefix(p)
			}
			s, _ := bld.BuildSet()
			v4, _ := s.Spans()
			return v4, s
		})
		measure("Table", func() (int, any) {
			var bld Builder
			for _, p := range prefixes {
				bld.AddPrefix(p)
			}
			tb, _ := bld.BuildTable()
			v4, _ := tb.Spans()
			return v4, tb
		})
	}
}
