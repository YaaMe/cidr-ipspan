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

ns/op, minimum of three runs, Go 1.26 on darwin/arm64 (Apple M2):

| corpus | mix | **ipspan.Set** | bart | netipx | cidranger |
|---|---|---|---|---|---|
| aws | all hit | **24.3** | 31.2 | 72.3 | 219.9 |
| aws | half hit | **17.3** | 18.1 | 65.1 | 129.2 |
| aws | all miss | 3.0 | **2.7** | 51.8 | 23.1 |
| github | all hit | **29.8** | 31.9 | 83.2 | 220.5 |
| github | half hit | 19.4 | **19.0** | 70.0 | 130.5 |
| github | all miss | 3.0 | **2.7** | 52.2 | 23.1 |
| linode | all hit | **13.3** | 14.6 | 45.7 | 194.3 |
| linode | half hit | **11.7** | 12.0 | 42.2 | 115.8 |
| linode | all miss | 3.0 | **2.7** | 33.9 | 23.3 |

**`ipspan.Set` is the fastest membership structure here on hits**, on all three
shapes including the fragmented one, and ties on half-hit mixes. bart keeps a
small edge on pure misses, 2.7 against 3.0.

## Longest-prefix match

Only two of these answer which prefix matched.

| corpus | mix | ipspan.Table | **bart** |
|---|---|---|---|
| aws | all hit | 65.8 | **37.0** |
| aws | half hit | 38.3 | **21.1** |
| aws | all miss | **3.3** | 3.5 |
| github | all hit | 45.1 | **34.0** |
| github | half hit | 27.3 | **20.4** |
| linode | all hit | 110.9 | **14.8** |
| linode | half hit | 60.0 | **12.1** |

**bart wins longest-prefix match, and on `linode` it is not close** — 14.8
against 110.9.

That is the same trade seen from the other side. Membership lets a structure
throw information away, and `linode`'s 5409 prefixes really are 95 spans, so
`Set` gets faster the more the data collapses. Longest-prefix match forbids
throwing anything away: every prefix must stay distinguishable, so `Table` has
to cut an interval wherever the winner changes and ends up with *more* pieces
than it started with. bart keeps the hierarchy instead of flattening it, and a
`/8` with a hundred `/24`s nested in it stays one node with a hundred children
rather than becoming two hundred intervals.

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

`ipspan.Table` is larger than bart as well as slower, so it is currently
dominated for its own use case. Roughly 2.6x of its size is slack rather than
necessity — `netip.Prefix` is 32 bytes where an IPv4 prefix needs five, and
intervals store both ends where contiguity means the next one's start implies
this one's end — but closing that would not obviously close a 7x speed gap.

## Reading this

Use `ipspan.Set` for membership; it is the fastest and smallest option here
that is not `netipx`, and it beats `netipx` on speed by a wide margin.

Use `bart` if you need to know which prefix matched. `ipspan.Table` works and
agrees with bart everywhere, but it has no case where it wins except pure
misses.
