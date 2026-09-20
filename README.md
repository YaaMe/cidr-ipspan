# ipspan

[![CI](https://github.com/YaaMe/cidr-ipspan/actions/workflows/ci.yml/badge.svg)](https://github.com/YaaMe/cidr-ipspan/actions/workflows/ci.yml)

Is this IP address in this set of CIDR blocks? For a set that is fixed once
built.

**For most uses, [gaissmai/bart](https://github.com/gaissmai/bart) is the
better choice.** `bart.Fast` is faster than this package on every hit workload
measured, and bart supports insert and delete where this is immutable once
built. Two narrower cases are what this is for; see
[Compared with other libraries](#compared-with-other-libraries).

```sh
go get github.com/YaaMe/cidr-ipspan
```

The module path and the package name differ — the module is `cidr-ipspan`, the
package is `ipspan` — so the import is worth naming explicitly.

```go
import ipspan "github.com/YaaMe/cidr-ipspan"

var b ipspan.Builder
b.AddPrefixString("52.95.110.0/24")
b.AddPrefixString("2620:107:300f::/48")
set, err := b.BuildSet()

set.Contains(netip.MustParseAddr("52.95.110.1")) // true
```

## What it does differently

It does not store the blocks.

A membership question carries no information that the covered address set does
not, so the blocks are merged into disjoint spans and the prefixes are thrown
away. Published provider ranges collapse hard under that:

| provider | IPv4 prefixes | spans | |
|---|---|---|---|
| linode | 5409 | **95** | 98% gone |
| aws | 7809 | **933** | 88% gone |
| digitalocean | 1081 | **101** | 91% gone |
| gcp | 1007 | **160** | 84% gone |
| github | 5745 | 2017 | 65% gone |

Duplicates, nesting and adjacency all disappear, and they are common: AWS
publishes the same prefix many times over, with plenty of `/24`s inside `/16`s.

What is left is small enough to **index directly rather than search**. A lookup
reads one array slot, which names the first span that could contain the
address, then compares against one or two spans. No hashing, no tree descent,
and — the point — no binary search.

That last part matters more than it sounds. `go4.org/netipx` also merges to
spans, and its footprint proves the merge is real: one byte per block on the
Linode list. It is still the slowest thing measured on a miss, because it
binary-searches, and that is `log n` unpredictable branches. **Collapsing the
set is worth a lot; searching it afterwards is where the saving goes.**

## Two structures, you choose

A `Set` forgets the prefixes. A `Table` remembers which one matched. Same
`Builder`, different `Build`:

```go
set, err := b.BuildSet()     // Contains(addr) bool
tbl, err := b.BuildTable()   // Lookup(addr) (netip.Prefix, bool)
                             // Contains(addr) bool, as a Set answers it
```

`Table.Contains` agrees with `Set.Contains` on the same `Builder`, always.
`Lookup` is the narrower question and can differ: `AddRange` covers addresses
without naming a prefix, so a range-only address is in `Contains` and absent
from `Lookup`. Ask `Contains` when the question is membership.

`Table` does not scan the prefixes covering an address. Since the set is fixed
once built, the answer is precomputed: prefix boundaries cut the address space
into elementary intervals, and inside one of those the longest match never
changes, so each interval stores its winner. Longest-prefix matching becomes
the same indexed read membership is.

ns/op, 8192 rotating addresses, **Go 1.26** on darwin/arm64 (Apple M2). Median
of 40 samples per case — two independent runs of 20, outliers rejected by IQR:

| blocks | mix | Set | Table.Contains | Table.Lookup |
|---|---|---|---|---|
| 1e3 | all hit | 5.7 | 5.7 | 6.9 |
| 1e3 | half hit | 8.6 | 8.8 | 9.5 |
| 1e3 | all miss | 3.7 | 3.7 | 4.3 |
| 1e5 | all hit | 4.4 | 19.0 | 20.8 |
| 1e5 | half hit | 8.1 | 15.5 | 16.3 |
| 1e5 | all miss | 3.7 | 3.7 | 4.3 |

The two runs agree within 2.2% on every cell, and the widest spread inside a
case is 11%. Compare within this table only.

The Go version matters more than it looks. The same benchmarks on Go 1.22 run
13–33% slower — 4.4 becomes 5.8 on the 1e5 hit — and none of that is the
Swiss-table map from Go 1.24, since nothing here uses a map. It is general
code generation. Numbers from this package are only comparable to others
measured on the same toolchain.

**Returning the prefix is nearly free.** `Table.Contains` stops as soon as it
knows an interval covers the address; `Lookup` also reads that interval's
winner and rebuilds the prefix, which costs 20.8 against 19.0 on the 1e5 hit.
Both are an indexed read rather than a search: what separates them is one more
array read and building the prefix, never a second pass over the data. Across
the table that is 5% to 21%, widest where the work itself is smallest.

Ask `Contains` when membership is the question. It is slightly cheaper, and it
is the only one of the two that answers for `AddRange`.

What a `Table` costs is intervals. Merging for membership joins everything
touching; a `Table` must also cut wherever the winner changes, so 100000
nested blocks give a `Set` 523 spans and a `Table` 183066 intervals. That is
where the 4.4 against 19.0 comes from, and the memory:

| blocks | Set | Table |
|---|---|---|
| 1e3 | 27 B/block | 81 B/block |
| 1e4 | 23 B/block | 83 B/block |
| 1e5 | 9 B/block | 66 B/block |

So: `Set` if membership is the question, `Table` if you need the answer. Misses
cost the same either way, which matters because for most callers misses are the
common case.

Neither is a routing table. There is no way to attach a value to a prefix, and
`Table` resolves one prefix rather than walking a hierarchy. For that, use a
trie such as [gaissmai/bart](https://github.com/gaissmai/bart).

## Why it cannot be mutated

A `Builder` accumulates; `BuildSet` or `BuildTable` merges and indexes; the
result is read-only and safe for concurrent use. Adding an address means
building again — the whole thing, from the blocks.

That is a consequence of the design rather than a missing feature, and it is
worth spelling out because "immutable" on its own reads like something nobody
got round to.

**Inserting is not local.** A trie mutates one node and is done, which is why
`bart` inserts in time proportional to the prefix length. Here the speed comes
from precomputing over the whole corpus, and all three parts of that are global:
the span array is sorted, so an insert is a memmove; the index holds a span
*number* per slot, so every entry after the insertion point shifts by one; and a
prefix outside the corpus's current extent changes the leading bits every span
shares, which rebuilds the index outright. Rebuilding the AWS list from scratch
takes 2.6 ms. That is nothing per hour and far too much per request.

**Deleting from a `Set` is not possible at all**, and the reason is the same one
that makes it fast. `10.0.0.0/8` and `10.1.0.0/16` become one span; remove the
`/16` and the `/8` still covers that range, but the span records neither, so
there is no way to know how much of it to give back. The information a delete
needs is exactly the information the merge threw away. Keeping it means keeping
the prefixes, at which point it is a `Table` — which can delete, by re-running
the sweep.

**What a mutable version would cost.** The usual escape is a small dynamic
overlay beside the static structure: insert into the overlay, query both,
rebuild when it fills. At 64 spans that amortises to roughly 40 µs per insert.
But every query then pays for the overlay scan, and queries being fast is the
only reason to choose this over a trie. Bentley–Saxe is worse for the same
reason — logarithmic amortised inserts, but a query touches all 17 levels at
100k blocks.

So if the set changes at runtime, use [gaissmai/bart](https://github.com/gaissmai/bart). Mutability is designed into
it; here it would be bolted onto the side of the thing that makes this fast.

## When it fits

Loaded once, queried constantly, answered yes or no: cloud provider ranges,
ACLs, blocklists, geo-IP membership, abuse filtering.

Check your own data rather than trusting the table above — `Spans()` reports
how far your blocks collapsed and `WorstScan()` the longest run of spans a
single lookup may walk before it gives up and searches instead. On provider
corpora that run reaches the hundreds for IPv6, which is why the walk has a
limit at all. A worst scan in the low single digits means the
index is doing its job; a large one means your spans crowd into one slot, which
is the shape this handles least well.

```go
v4, v6 := set.Spans()
fmt.Printf("%d IPv4 spans, worst scan %d\n", v4, must(set.WorstScan()))
```

## Status

Early. The lookup path and the merge are tested against an exhaustive linear
scan over sampled addresses — inside spans, on their edges, one address either
side, and far away — for corpora up to 20000 prefixes in both families, plus
the prefix lengths where the arithmetic changes shape (`/0`, `/32`, `/64`,
`/65`, `/128`, and the last address of each family).

One bug worth recording, since the test that caught it is the one worth
keeping. The interval sweep ordered a prefix's start before another's end at
the same address; because prefixes of one length share a slot in the active
set, the ending prefix then cleared the starting one and it vanished from part
of the table. Every individual lookup still returned a plausible prefix. Only
comparison against an exhaustive scan found it.

## Compared with other libraries

Measured against eight providers' published ranges in [bench/](bench/), which
has the full tables. ns/op, scattered probes, both address families, Go 1.26 on
darwin/arm64:

| corpus | fam | mix | ipspan.Set | bart.Lite | **bart.Fast** |
|---|---|---|---|---|---|
| aws | v4 | all hit | 39.7 | 51.7 | **33.0** |
| github | v4 | all hit | 54.8 | 54.2 | **32.7** |
| linode | v4 | all hit | 24.8 | 27.3 | **13.0** |
| aws | v4 | all miss | 6.5 | **4.7** | 5.1 |
| aws | **v6** | all hit | 93.6 | 100.9 | **56.8** |
| github | **v6** | all hit | 93.0 | 130.5 | **63.7** |
| linode | **v6** | all hit | 39.9 | 74.4 | **33.9** |
| aws | **v6** | all miss | **7.4** | 17.3 | 12.3 |

| | ipspan.Set | bart.Lite | bart.Fast |
|---|---|---|---|
| bytes per block (aws) | **24** | 33 | 112 |

**`bart.Fast` is faster than this package on every hit row**, by 1.2x to 1.9x,
and it costs 4.7x the memory to be. An earlier revision of this README claimed
IPv6 outright; that claim came from comparing only against `bart.Table` and
`bart.Lite`, and bart's author corrected it in
[cidrange-go#6](https://github.com/YaaMe/cidrange-go/issues/6).

So the case for this package is footprint, not peak speed. At the nearest
comparable footprint — `bart.Lite`, 33 bytes per block against 24 — `Set` wins
IPv6 clearly (93.0 against 130.5 on `github`) and ties IPv4, because a trie
pays for depth and provider IPv6 prefixes are deep, while a span lookup does
not care how long the prefix is. It also wins IPv6 pure misses outright, since
the index skips the leading bits every span shares and rejects an outside
address before reading a slot.

**For longest-prefix match, use bart.** `ipspan.Table` is slower than both bart
variants and larger than `bart.Table`; see [bench/](bench/) for the numbers. It
is kept because `BuildTable` costs nothing extra to offer once the spans exist,
not because there is a case for choosing it.

### So when is this worth picking

The architecture-independent difference is footprint. On the AWS corpus (11226
blocks) the three membership structures hold 0.26 MB, 0.36 MB and 1.20 MB —
`ipspan.Set`, `bart.Lite`, `bart.Fast`. For one set in a server process that
spread is about a megabyte and not worth thinking about: use `bart.Fast`. It
starts to matter when you hold many sets at once — per-tenant or per-customer
ACLs, where 4.7x the memory is multiplied by the number of sets you keep.

The other case is IPv6-heavy membership at that smaller footprint. Against
`bart.Lite`, the nearest structure by size, `Set` is ahead on IPv6 hits —
median of ten runs, darwin/arm64:

| v6, all hit | ipspan.Set | bart.Lite |
|---|---|---|
| aws | **93.8** | 100.4 |
| github | **93.2** | 129.5 |
| linode | **40.0** | 74.3 |

A trie pays for depth and provider IPv6 prefixes are deep, while a span lookup
does not care how long the prefix is. How far that carries to other
architectures is measured in CI rather than claimed here; see [bench/](bench/).

## Development

```sh
go test -race ./...                       # the library
go test -run '^$' -bench . ./...          # Set against Table
cd bench && go test -run '^$' -bench .    # against bart, netipx, cidranger
```

CI runs gofmt, vet, `go test -race` and a benchmark smoke run on Go 1.21, 1.24
and stable. The `bench` module is skipped on 1.21: it depends on bart, which
requires 1.24, and keeping that dependency out of the library is why the two
are separate modules at all.

## License

MIT.
