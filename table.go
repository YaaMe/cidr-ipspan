package ipspan

import (
	"math/bits"
)

// A span is a closed interval of addresses. Spans in a table are disjoint and
// sorted, so at most one can contain any address.
type span4 struct{ lo, hi uint32 }
type span6 struct{ lo, hi u128 }

// table4 and table6 are sorted disjoint spans plus an index over a window of
// the address bits.
//
// first[slot] is the earliest span that could contain any address in that slot:
// the first span whose end is at or after the slot's start. A lookup begins
// there and walks forward, stopping as soon as a span starts beyond the
// address. Because the spans are disjoint, the walk is short unless many spans
// crowd into one slot.
//
// The window does not start at the most significant bit. A real corpus occupies
// a small part of the address space — every global-unicast IPv6 prefix begins
// 001, and one provider's typically share far more than that — so indexing from
// bit zero spends most of its slots on addresses that cannot occur and crams
// the corpus into a handful. The leading bits that every span shares are
// checked once and skipped, and the index covers the bits after them, which
// buys 2^skip times the resolution for the same memory. Checking them also
// rejects an address outside the corpus's range before any slot is read.
//
// The index is sized to the data rather than fixed. A constant 2^16 table would
// be half a megabyte, absurd beside the kilobyte of spans a provider list
// typically collapses to, and too coarse for one that does not collapse.
type table4 struct {
	spans []span4
	first []int32

	skip     uint   // leading bits shared by every span
	skipVal  uint32 // those bits, in place
	skipMask uint32
	shift    uint // right shift taking an address to its slot
	slotMask uint32
}

type table6 struct {
	spans []span6
	first []int32

	skip     uint
	skipVal  u128
	skipMask u128
	shift    uint // applies to hi; skip+nbits is kept within 64
	slotMask uint64
}

// indexBits picks how many bits of the window to index on.
//
// Enough slots that spans rarely share one, but never so many that the index
// dwarfs what it indexes: four slots per span, rounded up to a power of two,
// clamped to a byte at the small end and 2^16 at the large. Measured across
// provider corpora, four is the knee — two costs about 20% in lookup time and
// eight buys about 2%.
func indexBits(spans int) uint {
	if spans == 0 {
		return 8
	}
	b := uint(bits.Len(uint(spans*4 - 1)))
	if b < 8 {
		return 8
	}
	if b > 16 {
		return 16
	}
	return b
}

// How far a lookup walks before falling back to a binary search.
//
// The index normally leaves one or two spans to check, and a short walk beats a
// search outright: no unpredictable branches, and the spans are contiguous. But
// a corpus can cluster more tightly than the index can separate — IPv6 provider
// ranges do, crowding into a handful of /16s — and then the walk becomes
// hundreds of comparisons. The fallback caps that at a logarithm.
//
// The two families want different limits, and the reason is cache lines: a
// span4 is 8 bytes so eight of them are one line, while a span6 is 32 and eight
// are four lines. Walking is four times as expensive per step for IPv6, so it
// gives up sooner. Measured on provider corpora, IPv4 at a limit of 8 is
// actually worse than never searching at all — 23.9 ns against 16.8 — while
// IPv6 with no limit costs 89 ns against 28.
const (
	linearLimit4 = 32
	linearLimit6 = 8
)

// commonBits32 counts the leading bits shared by the whole corpus.
func commonBits32(lo, hi uint32) uint {
	return uint(bits.LeadingZeros32(lo ^ hi))
}

func buildTable4(spans []span4) table4 {
	if len(spans) == 0 {
		return table4{}
	}
	nbits := indexBits(len(spans))

	// Skip what everything shares, but leave room for the window.
	skip := commonBits32(spans[0].lo, spans[len(spans)-1].hi)
	if skip+nbits > 32 {
		skip = 32 - nbits
	}

	t := table4{
		spans:    spans,
		skip:     skip,
		shift:    32 - skip - nbits,
		slotMask: 1<<nbits - 1,
		first:    make([]int32, 1<<nbits),
	}
	if skip > 0 {
		t.skipMask = ^uint32(0) << (32 - skip)
		t.skipVal = spans[0].lo & t.skipMask
	}

	// Walk slots and spans together. A span belongs to every slot from the one
	// holding its start onwards, until a later span takes over.
	next := 0
	for slot := range t.first {
		slotStart := t.skipVal | uint32(slot)<<t.shift
		for next < len(spans) && spans[next].hi < slotStart {
			next++
		}
		t.first[slot] = int32(next)
	}
	return t
}

// find returns the index of the span containing v, or -1. Returning the index
// rather than a bool is what lets Table share this path with Set: membership
// costs the same either way, and only resolving the prefix costs more.
func (t *table4) find(v uint32) int32 {
	if len(t.spans) == 0 || v&t.skipMask != t.skipVal {
		return -1
	}
	n := int32(len(t.spans))
	i := t.first[v>>t.shift&t.slotMask]
	for stop := i + linearLimit4; i < n && i < stop; i++ {
		s := &t.spans[i]
		if v < s.lo {
			return -1
		}
		if v <= s.hi {
			return i
		}
	}
	if i >= n {
		return -1
	}
	// The run under this slot is long, which happens when a corpus clusters
	// more tightly than the index can separate. Everything from here on has
	// lo <= v or the scan would have stopped, and the spans are disjoint and
	// sorted, so the only possible container is the last one starting at or
	// before v.
	lo, hi := i, n
	for lo < hi {
		mid := (lo + hi) / 2
		if t.spans[mid].lo <= v {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo > i && v <= t.spans[lo-1].hi {
		return lo - 1
	}
	return -1
}

func (t *table4) contains(v uint32) bool { return t.find(v) >= 0 }

func (t *table4) worstScan() int {
	worst := 0
	for slot := range t.first {
		slotEnd := t.skipVal | uint32(slot)<<t.shift | (1<<t.shift - 1)
		n := 0
		for i := t.first[slot]; i < int32(len(t.spans)) && t.spans[i].lo <= slotEnd; i++ {
			n++
		}
		if n > worst {
			worst = n
		}
	}
	return worst
}

// commonBits128 counts the leading bits shared by the whole corpus, capped so
// the caller can keep the index window inside the high word.
func commonBits128(lo, hi u128) uint {
	if d := lo.hi ^ hi.hi; d != 0 {
		return uint(bits.LeadingZeros64(d))
	}
	return 64 + uint(bits.LeadingZeros64(lo.lo^hi.lo))
}

func buildTable6(spans []span6) table6 {
	if len(spans) == 0 {
		return table6{}
	}
	nbits := indexBits(len(spans))

	// The window is kept inside the high word so a slot is one shift and one
	// mask. Providers share far more than 64 leading bits only in degenerate
	// corpora, where the extra resolution would buy nothing anyway.
	skip := commonBits128(spans[0].lo, spans[len(spans)-1].hi)
	if skip+nbits > 64 {
		skip = 64 - nbits
	}

	t := table6{
		spans:    spans,
		skip:     skip,
		shift:    64 - skip - nbits,
		slotMask: 1<<nbits - 1,
		first:    make([]int32, 1<<nbits),
	}
	if skip > 0 {
		t.skipMask = u128{hi: ^uint64(0) << (64 - skip)}
		t.skipVal = u128{hi: spans[0].lo.hi & t.skipMask.hi}
	}

	next := 0
	for slot := range t.first {
		slotStart := u128{hi: t.skipVal.hi | uint64(slot)<<t.shift}
		for next < len(spans) && spans[next].hi.less(slotStart) {
			next++
		}
		t.first[slot] = int32(next)
	}
	return t
}

func (t *table6) find(v u128) int32 {
	if len(t.spans) == 0 || v.hi&t.skipMask.hi != t.skipVal.hi {
		return -1
	}
	n := int32(len(t.spans))
	i := t.first[v.hi>>t.shift&t.slotMask]
	for stop := i + linearLimit6; i < n && i < stop; i++ {
		s := &t.spans[i]
		if v.less(s.lo) {
			return -1
		}
		if !s.hi.less(v) {
			return i
		}
	}
	if i >= n {
		return -1
	}
	// See table4.find: a long run falls back to a search rather than walking it.
	lo, hi := i, n
	for lo < hi {
		mid := (lo + hi) / 2
		if !v.less(t.spans[mid].lo) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo > i && !t.spans[lo-1].hi.less(v) {
		return lo - 1
	}
	return -1
}

func (t *table6) contains(v u128) bool { return t.find(v) >= 0 }

func (t *table6) worstScan() int {
	worst := 0
	for slot := range t.first {
		slotEnd := u128{
			hi: t.skipVal.hi | uint64(slot)<<t.shift | (1<<t.shift - 1),
			lo: ^uint64(0),
		}
		n := 0
		for i := t.first[slot]; i < int32(len(t.spans)) && !slotEnd.less(t.spans[i].lo); i++ {
			n++
		}
		if n > worst {
			worst = n
		}
	}
	return worst
}
