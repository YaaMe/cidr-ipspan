# ipspan

Is this IP address in this set of CIDR blocks? For a set that is fixed once
built.

```go
var b ipspan.Builder
b.AddPrefixString("52.95.110.0/24")
b.AddPrefixString("2620:107:300f::/48")
set, err := b.Build()

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

## The trade

**It answers membership and nothing else.** There is no way to ask which prefix
matched, or to attach a value to one.

That is not an omission, it is the mechanism. Merging assumes every covered
address is equivalent; longest-prefix matching assumes covered addresses are
ranked. `10.0.0.0/8` and `10.1.2.0/24` become one span, and `10.1.2.5` is in
both — the span records neither, only that the address is covered.

If you need the matching prefix back, keep a side table from span to
contributing prefixes: membership stays fast and the rarer question costs a
scan, but you are storing the prefixes again and the memory saving goes with
them. If you need longest-prefix match with a payload — a routing table — use a
trie such as [gaissmai/bart](https://github.com/gaissmai/bart).

**The set is immutable.** A `Builder` accumulates; `Build` merges and indexes;
the `Set` is read-only and safe for concurrent use. Adding an address means
building again.

## When it fits

Loaded once, queried constantly, answered yes or no: cloud provider ranges,
ACLs, blocklists, geo-IP membership, abuse filtering.

Check your own data rather than trusting the table above — `Spans()` reports
how far your blocks collapsed and `WorstScan()` the longest run of spans a
single lookup can compare. A worst scan in the low single digits means the
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

Not yet done: benchmarks in the repository, and a comparison against `bart`,
`netipx` and `cidranger` on real provider corpora. A prototype of this design
measured 15.0 ns against bart's 42.8 on a scattered all-hit workload, which is
what motivated writing it properly, but that figure is from the prototype and
not from this code.

## License

MIT.
