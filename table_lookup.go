package ipspan

import (
	"net/netip"
	"sort"
)

// Table answers which prefix an address matched, not merely whether one did.
//
// It does not scan the prefixes covering an address. Because the set is fixed
// once built, the answer can be precomputed: the prefix boundaries cut the
// address space into elementary intervals, and inside one of those the longest
// matching prefix never changes. Storing the winner per interval turns
// longest-prefix matching into the same lookup Set performs — one indexed array
// read and a comparison — rather than a search.
//
// The first attempt hung the covering prefixes off each merged span and scanned
// them. That collapses on exactly the data this package targets: 20000 nested
// IPv4 prefixes merge into five spans, one of which then held 15342 prefixes to
// scan. Precomputing removes the scan entirely.
//
// The cost against Set is intervals, not scans. Merging for membership joins
// everything touching; here an interval must also end wherever the winner
// changes, so a nested set yields more intervals than spans — bounded by twice
// the number of prefixes. Spans and WorstScan report what a given corpus
// actually produced.
//
// A Table is safe for concurrent use.
type Table struct {
	v4    table4
	v4Win []int32 // parallel to v4.spans: index into pfx, or -1
	v6    table6
	v6Win []int32
	pfx   []netip.Prefix
}

// Contains reports whether addr is covered by anything the Builder was given,
// including an AddRange, which has no prefix to report.
//
// It therefore agrees with Set.Contains on the same input, which is the point
// of the two being built from one Builder. It cannot be derived from Lookup:
// an address covered only by a range has no prefix to return, so Lookup says
// false where this says true.
func (t *Table) Contains(addr netip.Addr) bool {
	if t == nil {
		return false
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if addr.Is4() {
		return t.v4.contains(beUint32(addr.As4()))
	}
	if !addr.IsValid() {
		return false
	}
	return t.v6.contains(u128FromBytes(addr.As16()))
}

// Lookup returns the longest prefix containing addr.
//
// Longest is the meaningful answer when prefixes nest, which published lists do
// constantly: asked about an address inside both a /16 and a /24, a caller
// wants the /24.
//
// An address covered only by an AddRange has no prefix, so this reports false
// for it while Contains reports true. Use Contains for membership.
func (t *Table) Lookup(addr netip.Addr) (netip.Prefix, bool) {
	if t == nil {
		return netip.Prefix{}, false
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	var w int32
	if addr.Is4() {
		i := t.v4.find(beUint32(addr.As4()))
		if i < 0 {
			return netip.Prefix{}, false
		}
		w = t.v4Win[i]
	} else {
		if !addr.IsValid() {
			return netip.Prefix{}, false
		}
		i := t.v6.find(u128FromBytes(addr.As16()))
		if i < 0 {
			return netip.Prefix{}, false
		}
		w = t.v6Win[i]
	}
	if w < 0 {
		return netip.Prefix{}, false
	}
	return t.pfx[w], true
}

// LookupString parses s and returns the longest prefix containing it.
func (t *Table) LookupString(s string) (netip.Prefix, bool) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	return t.Lookup(addr)
}

// Spans reports how many elementary intervals the table holds per family.
// Compare with a Set built from the same blocks: the excess is what remembering
// the prefix costs.
func (t *Table) Spans() (v4, v6 int) {
	if t == nil {
		return 0, 0
	}
	return len(t.v4.spans), len(t.v6.spans)
}

// WorstScan reports the most intervals a single lookup can compare.
func (t *Table) WorstScan() (v4, v6 int) {
	if t == nil {
		return 0, 0
	}
	return t.v4.worstScan(), t.v6.worstScan()
}

// BuildTable builds a Table, which answers which prefix matched.
//
// Use BuildSet instead when membership is all you need: a Set discards the
// prefixes and is smaller for it.
func (b *Builder) BuildTable() (*Table, error) {
	t := &Table{}
	var spans4 []span4
	var spans6 []span6
	spans4, t.v4Win, t.pfx = sweep4(b.v4Prefixes, b.v4, t.pfx)
	spans6, t.v6Win, t.pfx = sweep6(b.v6Prefixes, b.v6, t.pfx)
	t.v4 = buildTable4(spans4)
	t.v6 = buildTable6(spans6)
	return t, b.err
}

// event marks where a prefix starts or stops covering the address line.
type event4 struct {
	at   uint32
	bits int
	idx  int32 // into the shared prefix table
	add  bool
}

// sweep4 cuts the address line at every prefix boundary and records the winner
// in each resulting interval.
//
// At any address, prefixes of the same length that contain it are necessarily
// the same prefix, so the active set holds at most one entry per length and the
// winner is simply the longest length still active. That is what keeps the
// sweep linear instead of needing a priority queue.
func sweep4(pfx []prefix4, raw []span4, table []netip.Prefix) ([]span4, []int32, []netip.Prefix) {
	if len(pfx) == 0 && len(raw) == 0 {
		return nil, nil, table
	}

	base := int32(len(table))
	events := make([]event4, 0, 2*len(pfx))
	for i, p := range pfx {
		table = append(table, p.p)
		hi := p.lo | hostMask32(p.p.Bits())
		events = append(events, event4{at: p.lo, bits: p.p.Bits(), idx: base + int32(i), add: true})
		if hi != ^uint32(0) {
			events = append(events, event4{at: hi + 1, bits: p.p.Bits(), idx: base + int32(i), add: false})
		}
	}
	// Ranges added by AddRange carry no prefix; they still cover addresses, so
	// they open and close an interval whose winner is none.
	const noPrefix = -1
	for _, s := range raw {
		events = append(events, event4{at: s.lo, bits: -1, idx: noPrefix, add: true})
		if s.hi != ^uint32(0) {
			events = append(events, event4{at: s.hi + 1, bits: -1, idx: noPrefix, add: false})
		}
	}
	// Removes come before adds at the same address. Only one prefix of a given
	// length can cover any address, so they share a slot in the active set; if
	// a prefix ending here were removed after the one beginning here was added,
	// the remove would clear the newcomer's slot and lose it entirely.
	sort.Slice(events, func(i, j int) bool {
		if events[i].at != events[j].at {
			return events[i].at < events[j].at
		}
		return !events[i].add && events[j].add
	})

	var active [33]int32 // per prefix length, the covering prefix, or -1
	for i := range active {
		active[i] = -1
	}
	rangeDepth := 0 // AddRange coverage, which has no length

	var spans []span4
	var wins []int32
	cur := uint32(0)
	open := false
	curWin := int32(-1)

	winner := func() (int32, bool) {
		for n := 32; n >= 0; n-- {
			if active[n] >= 0 {
				return active[n], true
			}
		}
		if rangeDepth > 0 {
			return -1, true
		}
		return -1, false
	}

	flush := func(upto uint32) {
		if !open || upto < cur {
			return
		}
		// Extend rather than append when the winner has not changed, so a
		// boundary that does not alter the answer costs nothing.
		if n := len(spans); n > 0 && wins[n-1] == curWin && spans[n-1].hi+1 == cur {
			spans[n-1].hi = upto
			return
		}
		spans = append(spans, span4{cur, upto})
		wins = append(wins, curWin)
	}

	for i := 0; i < len(events); {
		at := events[i].at
		if open && at > cur {
			flush(at - 1)
		}
		for ; i < len(events) && events[i].at == at; i++ {
			e := events[i]
			switch {
			case e.bits < 0 && e.add:
				rangeDepth++
			case e.bits < 0:
				rangeDepth--
			case e.add:
				active[e.bits] = e.idx
			default:
				active[e.bits] = -1
			}
		}
		curWin, open = winner()
		cur = at
	}
	if open {
		flush(^uint32(0))
	}
	return spans, wins, table
}

type event6 struct {
	at   u128
	bits int
	idx  int32
	add  bool
}

func sweep6(pfx []prefix6, raw []span6, table []netip.Prefix) ([]span6, []int32, []netip.Prefix) {
	if len(pfx) == 0 && len(raw) == 0 {
		return nil, nil, table
	}

	base := int32(len(table))
	events := make([]event6, 0, 2*len(pfx))
	for i, p := range pfx {
		table = append(table, p.p)
		hi := p.lo.or(hostMask128(p.p.Bits()))
		events = append(events, event6{at: p.lo, bits: p.p.Bits(), idx: base + int32(i), add: true})
		if next, ok := hi.next(); ok {
			events = append(events, event6{at: next, bits: p.p.Bits(), idx: base + int32(i), add: false})
		}
	}
	for _, s := range raw {
		events = append(events, event6{at: s.lo, bits: -1, idx: -1, add: true})
		if next, ok := s.hi.next(); ok {
			events = append(events, event6{at: next, bits: -1, idx: -1, add: false})
		}
	}
	// Removes before adds, for the reason given in sweep4.
	sort.Slice(events, func(i, j int) bool {
		if events[i].at != events[j].at {
			return events[i].at.less(events[j].at)
		}
		return !events[i].add && events[j].add
	})

	var active [129]int32
	for i := range active {
		active[i] = -1
	}
	rangeDepth := 0

	var spans []span6
	var wins []int32
	var cur u128
	open := false
	curWin := int32(-1)

	winner := func() (int32, bool) {
		for n := 128; n >= 0; n-- {
			if active[n] >= 0 {
				return active[n], true
			}
		}
		if rangeDepth > 0 {
			return -1, true
		}
		return -1, false
	}

	flush := func(upto u128) {
		if !open || upto.less(cur) {
			return
		}
		if n := len(spans); n > 0 && wins[n-1] == curWin {
			if next, ok := spans[n-1].hi.next(); ok && next == cur {
				spans[n-1].hi = upto
				return
			}
		}
		spans = append(spans, span6{cur, upto})
		wins = append(wins, curWin)
	}

	last := u128{^uint64(0), ^uint64(0)}
	for i := 0; i < len(events); {
		at := events[i].at
		if open && cur.less(at) {
			prev, _ := at.prev()
			flush(prev)
		}
		for ; i < len(events) && events[i].at == at; i++ {
			e := events[i]
			switch {
			case e.bits < 0 && e.add:
				rangeDepth++
			case e.bits < 0:
				rangeDepth--
			case e.add:
				active[e.bits] = e.idx
			default:
				active[e.bits] = -1
			}
		}
		curWin, open = winner()
		cur = at
	}
	if open {
		flush(last)
	}
	return spans, wins, table
}
