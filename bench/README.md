# bench

Published provider IP range corpora, and the comparison that runs over them.

A separate module: the comparison depends on `gaissmai/bart`, which requires Go
1.24, plus two more libraries. None of that should reach a caller who only
wants `ipspan`, and keeping the corpora here keeps half a megabyte of data out
of every `go get`.

```sh
./fetch.sh                          # refresh corpora from each provider
go test -v ./...                    # agreement and footprint
go test -run '^$' -bench . ./...    # the comparison
```

All five structures are built from the same corpus and checked to agree on 4000
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

| corpus | fam | mix | **ipspan.Set** | bart | netipx | cidranger |
|---|---|---|---|---|---|---|
| aws | v4 | all hit | **23.6** | 31.2 | — | — |
| aws | v4 | half hit | **15.4** | 17.6 | — | — |
| aws | v4 | all miss | 3.6 | **2.7** | — | — |
| github | v4 | all hit | 32.3 | **32.0** | — | — |
| linode | v4 | all hit | **14.4** | 14.6 | — | — |
| linode | v4 | half hit | **10.8** | 12.1 | — | — |
| aws | **v6** | all hit | **54.3** | 60.1 | — | — |
| aws | **v6** | half hit | **33.1** | 40.9 | — | — |
| aws | **v6** | all miss | **4.2** | 8.9 | — | — |
| github | **v6** | all hit | **53.0** | 76.8 | — | — |
| github | **v6** | half hit | **32.9** | 40.7 | — | — |
| linode | **v6** | all hit | **23.0** | 41.8 | — | — |
| linode | **v6** | half hit | **18.3** | 23.7 | — | — |

netipx and cidranger are omitted from this table for width; they run 45–220 ns
on the same probes, and the full set is in the benchmark output.

**IPv4 is close.** `ipspan.Set` wins on `aws`, ties on `github` and `linode`,
and loses the pure-miss case to bart at 3.6 against 2.7.

**IPv6 is not close.** `ipspan.Set` is ahead everywhere except one miss case —
23.0 against 41.8 on `linode`, 53.0 against 76.8 on `github`.

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

## Memory

bytes per block:

| corpus | ipspan.Set | ipspan.Table | bart | netipx | cidranger |
|---|---|---|---|---|---|
| aws | 24 | 90 | 71 | **14** | 503 |
| github | 22 | 83 | 67 | **18** | 508 |
| linode | 12 | 80 | 54 | — | 497 |

`ipspan.Set` costs a third of bart and falls as the data collapses — 24 bytes
per block on `aws`, 12 on `linode` — because what it stores is the *shape* of
the address set, not the blocks. `netipx` is smaller still and also merges to
ranges, but pays for it in the lookup: it binary-searches, and that is `log n`
unpredictable branches, which is why it is the slowest thing here on a miss
despite being the smallest.

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
