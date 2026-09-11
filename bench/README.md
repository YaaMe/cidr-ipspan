# bench

Published provider IP range corpora, and the comparison that runs over them.

A separate module: the comparison depends on `gaissmai/bart`, which requires Go
1.24, plus two more libraries. None of that should reach a caller who only
wants `ipspan`, and keeping the corpora here keeps half a megabyte of data out
of every `go get`.

```sh
./fetch.sh                          # refresh corpora from each provider
go test -v ./...                    # agreement
go test -run '^$' -bench . ./...    # the comparison
```

`BenchmarkBuild` reports construction time together with what each structure
*retains*, as a `B/block` metric. Go's own `B/op` counts allocation churn
during the build rather than what survives it, so retained size is measured
with `MemStats` and surfaced as a custom metric.

Everything here runs on Go 1.26, which `go.mod` pins: bart requires 1.24. The
toolchain is worth naming because it moves the numbers — the same benchmarks
on the library side run 13–33% slower on Go 1.22.

`bart` appears twice in the membership table. `bart.Lite` stores no payload,
which is what `ipspan.Set` does, so it is the fair opponent; `bart.Table` was
the original opponent and carried the ability to return a prefix while being
asked only for a boolean. They measure the same within noise — `Contains` never
reads the payload — so the distinction turned out not to matter, but it is kept
because that was worth establishing rather than assuming.

All structures are built from the same corpus and checked to agree on 4000
probes per provider before anything is timed — membership against an exhaustive
scan, and `ipspan.Table` against `bart` on *which* prefix, not merely whether
one exists. Probes rotate through 8192 addresses rather than repeating one.

Three providers are benchmarked as three shapes: `aws` large and heavily
collapsing, `github` fragmented, `linode` collapsing almost completely. All
eight are checked for correctness.

## Membership

ns/op, minimum of three runs, Go 1.26 on darwin/arm64 (Apple M2). Both families
are probed: the two index paths behave differently enough that measuring only
IPv4 hides half the picture.

| corpus | fam | mix | **ipspan.Set** | bart.Lite | bart.Table |
|---|---|---|---|---|---|
| aws | v4 | all hit | **23.5** | 30.9 | 31.4 |
| aws | v4 | half hit | **15.7** | 19.3 | 19.5 |
| aws | v4 | all miss | 3.6 | **2.7** | 2.7 |
| github | v4 | all hit | 32.7 | **31.7** | 32.5 |
| linode | v4 | all hit | 14.5 | **13.8** | 14.6 |
| linode | v4 | half hit | **10.7** | 11.8 | 12.1 |
| aws | **v6** | all hit | **53.9** | 60.1 | 59.8 |
| aws | **v6** | half hit | **33.5** | 41.3 | 40.3 |
| aws | **v6** | all miss | **4.2** | 8.4 | 9.3 |
| github | **v6** | all hit | **52.8** | 75.9 | 77.1 |
| github | **v6** | half hit | **33.0** | 41.2 | 40.9 |
| linode | **v6** | all hit | **23.0** | 40.5 | 41.8 |
| linode | **v6** | half hit | **18.2** | 24.2 | 23.9 |

netipx and cidranger are omitted from this table for width; they run 45–220 ns
on the same probes, and the full set is in the benchmark output.

**IPv4 is close.** `ipspan.Set` wins on `aws`, ties on `github` and `linode`,
and loses the pure-miss case at 3.6 against 2.7.

**IPv6 is not close.** `ipspan.Set` is ahead everywhere except pure misses —
23.0 against 40.5 on `linode`, 52.8 against 75.9 on `github`.

The asymmetry has the same cause as the advantage this design was built for.
A trie descends one node per stride, so its cost grows with how deep the match
lies, and IPv6 provider prefixes are deep: AWS publishes mostly `/40`s, with
`/48`s and `/64`s besides. A span lookup does not care how long the prefix is.

## Longest-prefix match

Only two of these answer which prefix matched.

| corpus | fam | mix | ipspan.Table | bart |
|---|---|---|---|---|
| aws | v4 | all hit | 40.2 | **37.0** |
| github | v4 | all hit | 44.4 | **34.0** |
| linode | v4 | all hit | 62.3 | **14.8** |
| linode | v4 | half hit | 34.9 | **12.2** |
| aws | v6 | all hit | 62.6 | **54.6** |
| aws | v6 | half hit | **37.0** | 38.8 |
| aws | v6 | all miss | **4.5** | 10.5 |
| github | v6 | all hit | **62.1** | 72.5 |
| github | v6 | half hit | **37.2** | 39.4 |
| linode | v6 | all hit | **39.8** | 41.7 |

**bart wins IPv4 decisively** — 14.8 against 62.3 on `linode`. IPv6 is a draw,
with `Table` ahead on half-hit mixes and misses and behind on pure hits.

The IPv4 result is the same trade seen from the other side. Membership lets a
structure throw information away, and `linode`'s 5409 prefixes really are 95
spans, so `Set` gets faster the more the data collapses. Longest-prefix match
forbids throwing anything away: every prefix must stay distinguishable, so
`Table` cuts an interval wherever the winner changes and ends up with *more*
pieces than it started with — exactly where `Set` ends up with fewest. bart
keeps the hierarchy instead of flattening it: a `/8` with a hundred `/24`s
nested inside stays one node with a hundred children rather than two hundred
intervals.

IPv6 holds up better because bart pays for depth there, and that partly offsets
the flattening.

## Memory and build cost

From `BenchmarkBuild`. Bytes per block is what the structure retains, not what
it allocated getting there:

| corpus | | ipspan.Set | ipspan.Table | bart.Lite | bart.Table | netipx | cidranger |
|---|---|---|---|---|---|---|---|
| aws | B/block | 24.1 | 89.8 | 33.3 | 71.4 | **13.9** | 502.5 |
| aws | build ms | 2.6 | 6.9 | **1.9** | 2.4 | 3.6 | 29.7 |
| github | B/block | 22.5 | 82.6 | 26.9 | 67.3 | **18.0** | 508.3 |
| github | build ms | 1.4 | 4.6 | **1.2** | 1.4 | 2.1 | 17.7 |
| linode | B/block | 11.8 | 79.6 | 21.6 | 53.9 | **1.1** | 497.1 |
| linode | build ms | 1.0 | 3.0 | **0.7** | 0.9 | 1.6 | 10.5 |

`ipspan.Set` is smaller than `bart.Lite` on every corpus and falls as the data
collapses — 24 bytes per block on `aws`, 12 on `linode` — because what it
stores is the *shape* of the address set rather than the blocks. `netipx` is
smaller still, and its 1.1 bytes per block on `linode` is the clearest evidence
that the collapse is real: 5505 prefixes really are 95 spans. It pays for that
in the lookup, binary-searching where this indexes.

Build cost tracks it: `ipspan.Set` takes about a third longer than `bart.Lite`
because merging needs a sort, and `ipspan.Table` two to three times that again
because the sweep emits far more intervals. All of them are milliseconds for a
whole provider list, which for a structure built once is not the interesting
column.

`ipspan.Table` is larger than bart as well as slower on IPv4. Roughly 2.6x of
its size is slack rather than necessity — `netip.Prefix` is 32 bytes where an
IPv4 prefix needs five, and intervals store both ends where contiguity means
the next one's start implies this one's end — but closing that would not close a
4x speed gap on `linode`.

## Reading this

**Membership:** use `ipspan.Set`. It wins or ties on IPv4 and wins IPv6 clearly,
at a third of bart's memory. bart keeps a small edge on pure IPv4 misses, 2.7
against 3.6.

**Which prefix matched, IPv4:** use bart.

**Which prefix matched, IPv6:** either. `ipspan.Table` is ahead on mixed and
miss-heavy workloads, bart on pure hits.
