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

## Two structures, you choose

A `Set` forgets the prefixes. A `Table` remembers which one matched. Same
`Builder`, different `Build`:

```go
set, err := b.BuildSet()     // Contains(addr) bool
tbl, err := b.BuildTable()   // Lookup(addr) (netip.Prefix, bool)
```

`Table` does not scan the prefixes covering an address. Since the set is fixed
once built, the answer is precomputed: prefix boundaries cut the address space
into elementary intervals, and inside one of those the longest match never
changes, so each interval stores its winner. Longest-prefix matching becomes
the same indexed read membership is.

ns/op, 8192 rotating addresses, darwin/arm64 (Apple M2), minimum of three runs:

| blocks | mix | Set | Table.Contains | Table.Lookup |
|---|---|---|---|---|
| 1e3 | all hit | 5.9 | 7.2 | 7.4 |
| 1e3 | half hit | 9.5 | 10.3 | 10.5 |
| 1e3 | all miss | 4.2 | 4.5 | 4.5 |
| 1e5 | all hit | 4.6 | 22.1 | 21.8 |
| 1e5 | half hit | 10.0 | 15.5 | 15.8 |
| 1e5 | all miss | 4.2 | 4.5 | 4.5 |

**Returning the prefix is free.** `Lookup` costs what `Table.Contains` costs —
21.8 against 22.1 — because the winner is one more array read, not a search.
Once you are paying for a `Table`, there is no reason to ask the weaker
question.

What a `Table` costs is intervals. Merging for membership joins everything
touching; a `Table` must also cut wherever the winner changes, so 100000
nested blocks give a `Set` 523 spans and a `Table` 183066 intervals. That is
where the 4.6 against 22.1 comes from, and the memory:

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

**Both are immutable.** A `Builder` accumulates; `BuildSet` or `BuildTable`
merges and indexes; the result is read-only and safe for concurrent use. Adding
an address means building again.

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

One bug worth recording, since the test that caught it is the one worth
keeping. The interval sweep ordered a prefix's start before another's end at
the same address; because prefixes of one length share a slot in the active
set, the ending prefix then cleared the starting one and it vanished from part
of the table. Every individual lookup still returned a plausible prefix. Only
comparison against an exhaustive scan found it.

Not yet done: a comparison against `bart`, `netipx` and `cidranger` on real
provider corpora. The numbers above are against this package's own synthetic
corpora and say nothing about how it compares. A prototype measured 15.0 ns
against bart's 42.8 on a scattered all-hit workload, which is what motivated
writing this properly, but that figure belongs to the prototype and the index
strategy has changed since.

## License

MIT.
