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

ns/op, median of ten samples after IQR outlier rejection, Go 1.26 on
darwin/arm64 (Apple M2). Both families are probed: the two index paths behave
differently enough that measuring only IPv4 hides half the picture — and in
`cidrange`'s case it hid a 24x pathology, see below.

Everything in this table was measured **in one process, against the same
probes and the same structures**. `cidrange` is the sibling repo this package
grew out of; the two used to be benchmarked separately and were not comparable,
because each configured bart differently.

| corpus | fam | mix | cidrange | ipspan.Set | bart.Lite | bart.Table | **bart.Fast** |
|---|---|---|---|---|---|---|---|
| aws | v4 | all hit | 116.6 | 40.1 | 54.0 | 54.9 | **34.0** |
| aws | v4 | half hit | 67.7 | 27.6 | 31.7 | 32.2 | **23.2** |
| aws | v4 | all miss | 5.3 | 6.5 | **4.7** | 5.1 | 5.1 |
| aws | **v6** | all hit | 162.3 | 94.3 | 100.2 | 101.5 | **56.8** |
| aws | **v6** | half hit | 175.7 | 56.0 | 69.2 | 70.1 | **39.0** |
| aws | **v6** | all miss | 176.0 | **7.4** | 17.5 | 18.2 | 12.3 |
| github | v4 | all hit | 116.6 | 54.9 | 54.7 | 55.3 | **33.8** |
| github | v4 | half hit | 67.1 | 34.1 | 32.7 | 33.1 | **24.0** |
| github | **v6** | all hit | 94.4 | 93.1 | 129.3 | 129.6 | **63.5** |
| github | **v6** | half hit | 58.2 | 56.0 | 70.3 | 70.5 | **38.0** |
| github | **v6** | all miss | 6.6 | 7.4 | **4.9** | 5.1 | 5.1 |
| linode | v4 | all hit | 35.3 | 24.7 | 27.2 | 27.4 | **13.1** |
| linode | **v6** | all hit | 90.4 | 40.1 | 74.3 | 75.1 | **34.0** |
| linode | **v6** | half hit | 54.1 | 30.3 | 43.4 | 43.4 | **24.8** |

**`bart.Fast` is fastest on every hit row**, by 1.2x to 1.9x over `ipspan.Set`.
It pays for that in memory; see below.

**Against `bart.Lite`** — the nearest structure by footprint — `ipspan.Set` wins
every IPv6 hit row (93.1 against 129.3 on `github`, 40.1 against 74.3 on
`linode`) and is level on IPv4.

**On pure misses `bart.Lite` wins**, 4.7–4.9 against 7.4. An earlier revision of
this file claimed the opposite from the `aws` v6 row alone, where `Set`'s 7.4
does beat `Lite`'s 17.5. That was one row generalised to a rule; `github` and
`linode` go the other way.

`Lite` and `Table` are within 2% of each other on *speed*, because `Contains`
never reads the payload — but that is not what separates them. bart's author
[made the point](https://github.com/YaaMe/cidrange-go/issues/6) that `Lite`
exists for its **memory**: its nodes carry no room for a payload at all. The
table below has it at 33 B/block against `Table`'s 71 on `aws`, and 15.0 MB
against 15.3 on a routing table. Saying the distinction "changes no figure"
was true of one axis and wrong about the other.

**`cidrange` collapses on AWS IPv6** — 176 ns, and misses are no cheaper than
hits, so the early exit is not firing and the lookup degrades to a scan. It is
24x `ipspan.Set` on that row. Its own benchmarks probe IPv4 only and never saw
it. The same corpus is where its footprint is worst, so mask-length bucketing
is what does not survive AWS's mix of `/40`, `/48` and `/64`.

## Longest-prefix match

Only these three answer which prefix matched. Same run as above.

| corpus | fam | mix | ipspan.Table | bart.Table | **bart.Fast** |
|---|---|---|---|---|---|
| aws | v4 | all hit | 70.5 | 70.0 | **43.5** |
| aws | v4 | half hit | 43.6 | 42.0 | **29.7** |
| aws | **v6** | all hit | 110.4 | 100.4 | **52.6** |
| aws | **v6** | half hit | 63.4 | 71.2 | **39.1** |
| aws | **v6** | all miss | **7.7** | 19.8 | 14.6 |
| github | v4 | all hit | 77.4 | 62.4 | **38.2** |
| github | **v6** | all hit | 109.8 | 128.3 | **62.2** |
| github | **v6** | half hit | 64.0 | 71.9 | **38.5** |
| linode | v4 | all hit | 107.7 | 28.6 | **14.1** |
| linode | **v6** | all hit | 70.3 | 77.3 | **34.5** |

**`bart.Fast` wins every row except IPv6 pure misses.** `ipspan.Table` has no
case left on speed — it loses IPv4 badly (107.7 against 14.1 on `linode`) and
IPv6 by roughly 2x.

The IPv4 gap is structural. Membership lets a structure throw information away,
and `linode`'s 5409 IPv4 prefixes really are 95 spans; longest-prefix match
forbids it, so `Table` cuts an interval wherever the winner changes and ends up
with more pieces than it started with — exactly where `Set` ends up with fewest.
bart keeps the hierarchy instead of flattening it.

## Memory and build cost

Retained bytes per block — what the structure holds, not what it allocated
getting there — with `runtime.KeepAlive` on both the structure and its input,
since without the latter the GC reclaims the source between samples and the
figures come out ~1.6x low. Cross-checked against `TestFootprint`, which
measures the same thing a different way and agrees to the byte.

| corpus | | cidrange | ipspan.Set | ipspan.Table | bart.Lite | bart.Table | bart.Fast | netipx | cidranger |
|---|---|---|---|---|---|---|---|---|---|
| aws | B/block | 106 | **24** | 90 | 33 | 71 | 112 | 14 | 502 |
| aws | build ms | 6.0 | 4.1 | 11.9 | **3.3** | 4.1 | 5.0 | 7.5 | 56.7 |
| github | B/block | 97 | **22** | 83 | 27 | 67 | 106 | 18 | 508 |
| github | build ms | 4.1 | 2.5 | 8.6 | **2.3** | 2.7 | 3.0 | 4.1 | 32.4 |
| linode | B/block | 136 | **12** | 80 | 22 | 54 | 59 | 1 | 497 |
| linode | build ms | 1.5 | 1.4 | 5.3 | **1.2** | 1.4 | 1.8 | 2.6 | 18.2 |

**This is what `bart.Fast` costs**: 112 bytes per block on `aws` against
`ipspan.Set`'s 24. It buys its speed with roughly 4.7x the memory, which is the
frame for every row in the tables above.

`ipspan.Set` is the smallest of the prefix-preserving structures and shrinks as
the data collapses, because what it stores is the shape of the address set
rather than the blocks. `netipx` is smaller still — 1 byte per block on
`linode` is the clearest evidence the collapse is real — and pays in the
lookup, binary-searching where this indexes.

`cidrange` is the only structure that gets *larger* on the corpus that collapses
hardest: 136 bytes per block on `linode` against 106 on `aws`, while every other
structure roughly halves. Bucketing by mask length does not benefit from
adjacency the way merging does.

## On a routing table

Everything above is provider allocation lists, which is the workload this
package was written for. They are also the shape that suits it: allocations
barely nest. A routing table is announcements inside announcements, which is
the case bart is built for, so it is the fairer test of whether the premise
generalises.

`bench/testdata/tier1-routes.txt.gz` is a full Internet routing table — 901,899
IPv4 prefixes and 160,147 IPv6 — from [gaissmai/iprbench], MIT, redistributed
with attribution. Unlike the provider corpora, which `fetch.sh` refreshes, and
unlike a licensed commercial database, which cannot be committed at all, this
one is fixed and public: figures taken from it can be checked by a reader.

**The collapse holds.**

| | prefixes | spans | |
|---|---|---|---|
| IPv4 | 901,899 | 67,888 | 92.5% gone |
| IPv6 | 160,147 | 40,658 | 74.6% gone |

Nesting turns out not to matter to it. A routing table nests heavily, but
nesting is about *who announced* an address — and membership does not ask that.
The address set underneath is still large contiguous runs, and 537,698 of the
IPv4 prefixes are `/24`s, many of them adjacent.

One structure per process, 1024 rotating probes, median of ten after outlier
rejection:

| | retained | lookup |
|---|---|---|
| **ipspan.Set** | **0.78 MB** | **6.5 ns** |
| bart.Lite | 14.97 MB | 11.3 ns |
| bart.Table | 15.34 MB | 10.8 ns |
| bart.Fast | 21.72 MB | 7.7 ns |

19x smaller and faster, on bart's own ground. Note `Lite` below `Table`, which
is the memory difference its existence is for.

**This corpus found a bug.** Merging returned a window into the input's backing
array rather than a copy, so a `Set` pinned every block it was built from for
its lifetime — 8.3 MB where 0.8 was needed. The overhead scaled with the block
count, which is to say it was worst exactly where this package is meant to be
used, and it was invisible on the 11,226-block corpora everything had been
measured on until now. No correctness test could have found it: every answer
was right.

[gaissmai/iprbench]: https://github.com/gaissmai/iprbench

## Reading this

**If memory is free, use `bart.Fast`.** Fastest on every hit workload here, for
both membership and longest-prefix match.

**If memory is not free**, the comparison is `ipspan.Set` at 24 B/block against
`bart.Fast` at 112 or `bart.Lite` at 33. Against `Lite`, `Set` wins every IPv6
hit row and ties IPv4, at three quarters of the footprint. That is the case
this package has.

**For longest-prefix match, use bart** on either family.

**On the absolute numbers.** They are not stable across machine states: the same
benchmarks here have produced figures 1.6x apart between a quiet session and a
loaded one, with every ratio between implementations unchanged. Compare within
one table, never across revisions of it, and treat the ratios as the claim that
transfers. Each figure is the median of ten samples with outliers rejected; the
worst residual spread in this run was 25%, on a row whose margin is far wider
than that.

