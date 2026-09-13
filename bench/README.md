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

ns/op, minimum of five runs, Go 1.26 on darwin/arm64 (Apple M2). Both families
are probed: the two index paths behave differently enough that measuring only
IPv4 hides half the picture.

`bart.Fast` was added after bart's author pointed out in
[YaaMe/cidrange-go#6](https://github.com/YaaMe/cidrange-go/issues/6) that the
earlier tables compared against the wrong variants. It changes the conclusion,
so it is reported first rather than buried.

| corpus | fam | mix | ipspan.Set | bart.Lite | bart.Table | **bart.Fast** |
|---|---|---|---|---|---|---|
| aws | v4 | all hit | 39.7 | 51.7 | 51.9 | **33.0** |
| aws | v4 | half hit | 26.8 | 30.6 | 29.9 | **22.8** |
| aws | v4 | all miss | 6.5 | **4.7** | 5.1 | 5.1 |
| aws | **v6** | all hit | 93.6 | 100.9 | 101.6 | **56.8** |
| aws | **v6** | half hit | 55.1 | 68.7 | 69.4 | **38.7** |
| aws | **v6** | all miss | **7.4** | 17.3 | 18.2 | 12.3 |
| github | v4 | all hit | 54.8 | 54.2 | 54.7 | **32.7** |
| github | v4 | half hit | 33.2 | 31.9 | 31.9 | **23.3** |
| github | **v6** | all hit | 93.0 | 130.5 | 129.6 | **63.7** |
| github | **v6** | half hit | 55.4 | 72.0 | 71.6 | **38.1** |
| linode | v4 | all hit | 24.8 | 27.3 | 27.4 | **13.0** |
| linode | v4 | half hit | 18.7 | 21.8 | 22.0 | **17.1** |
| linode | **v6** | all hit | 39.9 | 74.4 | 75.1 | **33.9** |
| linode | **v6** | half hit | 30.4 | 43.2 | 43.3 | **24.5** |

**`bart.Fast` is faster than `ipspan.Set` on every hit row**, by 1.2x to 1.9x.
An earlier version of this file claimed IPv6 for this package; against `Fast`
that claim is wrong, and it was wrong because the comparison was against
`bart.Table` and `bart.Lite` only.

Two rows survive, and they are the same row twice: `ipspan.Set` wins IPv6 pure
misses, 7.4 against 12.3. The index skips the leading bits every span shares,
so an address outside the corpus is rejected before any slot is read.

`Lite` and `Table` are within 2% of each other everywhere, because `Contains`
never reads the payload. That distinction, which the previous revision of this
file spent a paragraph on, turned out not to matter. `Fast` is the one that did.

netipx and cidranger are omitted for width; they run 45-220 ns on the same
probes, and the full set is in the benchmark output.

## Longest-prefix match

Only these three answer which prefix matched.

| corpus | fam | mix | ipspan.Table | bart.Table | **bart.Fast** |
|---|---|---|---|---|---|
| aws | v4 | all hit | 69.8 | 68.0 | **43.1** |
| aws | v4 | half hit | 42.0 | 41.4 | **28.8** |
| aws | **v6** | all hit | 109.6 | 99.9 | **52.6** |
| aws | **v6** | half hit | 62.4 | 71.4 | **38.6** |
| aws | **v6** | all miss | **7.7** | 19.8 | 14.6 |
| github | v4 | all hit | 77.1 | 62.2 | **38.2** |
| github | **v6** | all hit | 109.3 | 128.7 | **61.6** |
| github | **v6** | half hit | 63.1 | 71.8 | **38.3** |
| linode | v4 | all hit | 107.2 | 28.5 | **14.0** |
| linode | **v6** | all hit | 69.8 | 77.0 | **34.4** |

**`bart.Fast` wins every row except IPv6 pure misses.** `ipspan.Table` has no
case left on speed: it loses IPv4 badly (107.2 against 14.0 on `linode`) and
IPv6 by roughly 2x.

The IPv4 gap is structural, not a tuning problem. Membership lets a structure
throw information away, and `linode`'s 5409 IPv4 prefixes really are 95 spans;
longest-prefix match forbids it, so `Table` cuts an interval wherever the
winner changes and ends up with more pieces than it started with, exactly
where `Set` ends up with fewest. bart keeps the hierarchy instead of
flattening it.

## Memory and build cost

From `BenchmarkBuild`. Bytes per block is what the structure retains, not what
it allocated getting there; cross-checked against `TestFootprint`, which
measures the same thing a different way and agrees to the byte.

| corpus | | ipspan.Set | ipspan.Table | bart.Lite | bart.Table | bart.Fast | netipx | cidranger |
|---|---|---|---|---|---|---|---|---|
| aws | B/block | 24 | 90 | 33 | 71 | 112 | **14** | 502 |
| aws | build ms | 4.0 | 12.0 | 3.2 | 3.6 | 4.2 | 6.2 | 53.1 |
| github | B/block | 22 | 83 | 27 | 67 | 106 | **18** | 508 |
| github | build ms | 2.1 | 7.5 | **2.0** | 2.3 | 2.6 | 3.5 | 31.9 |
| linode | B/block | 12 | 80 | 22 | 54 | 59 | **1** | 497 |
| linode | build ms | 1.4 | 5.2 | **1.2** | 1.4 | 1.8 | 2.6 | 18.0 |

**This is what `bart.Fast` costs.** It is the largest structure in the table -
112 bytes per block on `aws`, against `ipspan.Set`'s 24. It buys its speed with
roughly 4.7x the memory, and that is the honest frame for every row above.

`ipspan.Set` is the smallest of the prefix-preserving structures and falls as
the data collapses, because what it stores is the shape of the address set
rather than the blocks. `netipx` is smaller still; its 1 byte per block on
`linode` is the clearest evidence the collapse is real. It pays in the lookup,
binary-searching where this indexes.

## Reading this

**If memory is free, use `bart.Fast`.** It is fastest on every workload here
except IPv6 pure misses, for both membership and longest-prefix match.

**If memory is not free**, the comparison is `ipspan.Set` at 24 B/block against
`bart.Fast` at 112, or `bart.Lite` at 33. Against `Lite` - the nearest
footprint - `Set` wins IPv6 clearly (93.0 against 130.5 on `github`) and ties
IPv4. That is the case this package still has.

**For longest-prefix match, use bart.** `ipspan.Table` costs more memory than
`bart.Table` and is slower than both bart variants.

**A caveat on the absolute numbers.** They are not stable across machine
states: the same benchmarks on this machine have produced figures 1.6x apart
between a quiet session and a loaded one, with the ratios between
implementations unchanged. Compare within a table, never across revisions of
one, and treat the ratios as the transferable claim.
