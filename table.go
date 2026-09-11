package ipspan

import "math/bits"

// A span is a closed interval of addresses. Spans in a table are disjoint and
// sorted, so at most one can contain any address.
type span4 struct{ lo, hi uint32 }
type span6 struct{ lo, hi u128 }

// table4 and table6 are sorted disjoint spans plus an index over the leading
// bits of the address.
//
// first[slot] is the earliest span that could contain any address in that slot:
// the first span whose end is at or after the slot's start. A lookup begins
// there and walks forward, which stops immediately once a span starts beyond
// the address. Because the spans are disjoint, the walk is one or two steps
// unless many spans crowd into one slot.
//
// The index is sized to the data rather than fixed. A fixed 2^16 table would be
// half a megabyte, absurd beside the 800 bytes of spans a provider list
// typically collapses to, and too coarse for one that does not collapse.
type table4 struct {
	spans []span4
	first []int32
	shift uint // address bits discarded to get a slot
}

type table6 struct {
	spans []span6
	first []int32
	shift uint
}

// indexBits picks how many leading bits to index on.
//
// Enough slots that spans rarely share one, but never so many that the index
// dwarfs the spans it indexes: four slots per span, rounded up to a power of
// two, clamped to a byte at the small end and to 2^16 at the large.
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

func buildTable4(spans []span4) table4 {
	if len(spans) == 0 {
		return table4{}
	}
	nbits := indexBits(len(spans))
	t := table4{spans: spans, shift: 32 - nbits, first: make([]int32, 1<<nbits)}

	// Walk slots and spans together. A span belongs to every slot from the one
	// holding its start onwards, until a later span takes over.
	next := 0
	for slot := range t.first {
		slotStart := uint32(slot) << t.shift
		for next < len(spans) && spans[next].hi < slotStart {
			next++
		}
		t.first[slot] = int32(next)
	}
	return t
}

func (t *table4) contains(v uint32) bool {
	if len(t.spans) == 0 {
		return false
	}
	for i := t.first[v>>t.shift]; i < int32(len(t.spans)); i++ {
		s := &t.spans[i]
		if v < s.lo {
			return false
		}
		if v <= s.hi {
			return true
		}
	}
	return false
}

func (t *table4) worstScan() int {
	worst := 0
	for slot := range t.first {
		slotEnd := uint32(slot)<<t.shift | (1<<t.shift - 1)
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

func buildTable6(spans []span6) table6 {
	if len(spans) == 0 {
		return table6{}
	}
	nbits := indexBits(len(spans))
	t := table6{spans: spans, shift: 128 - nbits, first: make([]int32, 1<<nbits)}

	next := 0
	for slot := range t.first {
		slotStart := u128{hi: uint64(slot) << (64 - nbits)}
		for next < len(spans) && spans[next].hi.less(slotStart) {
			next++
		}
		t.first[slot] = int32(next)
	}
	return t
}

func (t *table6) contains(v u128) bool {
	if len(t.spans) == 0 {
		return false
	}
	slot := v.hi >> (64 - (128 - t.shift))
	for i := t.first[slot]; i < int32(len(t.spans)); i++ {
		s := &t.spans[i]
		if v.less(s.lo) {
			return false
		}
		if !s.hi.less(v) {
			return true
		}
	}
	return false
}

func (t *table6) worstScan() int {
	nbits := 128 - t.shift
	worst := 0
	for slot := range t.first {
		slotEnd := u128{
			hi: uint64(slot)<<(64-nbits) | (1<<(64-nbits) - 1),
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
