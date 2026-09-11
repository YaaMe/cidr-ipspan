// Package ipspan answers whether an IP address falls inside a set of CIDR
// blocks, for a set that is fixed once built.
//
// It does not store the blocks. A membership question carries no information
// the covered address set does not, so the blocks are merged into disjoint
// spans and the prefixes are discarded. Published provider ranges collapse
// hard under that: 5409 Linode IPv4 prefixes are really 95 spans, and 7809 AWS
// ones are 933.
//
// What remains is small enough to index directly rather than search. A lookup
// is one array read, then a comparison against one or two spans — no hashing,
// no tree descent, and no branch-heavy binary search.
//
// Two structures are offered. A Set forgets the prefixes and answers only
// membership; a Table also records which prefix an address matched, at the cost
// of more intervals and more memory. Returning the prefix itself is free once
// you are paying for a Table, because the answer is precomputed per interval
// rather than searched for.
//
// Neither is a routing table: no value can be attached to a prefix. The set is
// also immutable, built once from a Builder; adding an address means building
// again.
package ipspan

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
)

// Set is an immutable set of IP addresses. The zero value is an empty set that
// contains nothing. Build one with a Builder.
//
// A Set is safe for concurrent use.
type Set struct {
	v4 table4
	v6 table6
}

// Builder accumulates CIDR blocks and address ranges. The zero value is ready
// to use.
//
// Nothing is merged or indexed until Build is called, so adding is cheap and
// order does not matter.
type Builder struct {
	v4 []span4
	v6 []span6

	// The original prefixes, kept only so BuildTable can file them under their
	// spans. BuildSet ignores them, and they are dropped with the Builder.
	v4Prefixes []prefix4
	v6Prefixes []prefix6

	err error
}

// prefix4 and prefix6 pair a prefix with its start address, so assigning it to
// a span is a comparison rather than a re-derivation.
type prefix4 struct {
	lo uint32
	p  netip.Prefix
}

type prefix6 struct {
	lo u128
	p  netip.Prefix
}

// AddPrefix adds every address in p. An invalid prefix is recorded as an error
// and reported by Build.
func (b *Builder) AddPrefix(p netip.Prefix) {
	if !p.IsValid() {
		b.setErr(fmt.Errorf("ipspan: invalid prefix %v", p))
		return
	}
	p = p.Masked()
	addr := p.Addr()
	if addr.Is4() {
		lo := beUint32(addr.As4())
		hi := lo | hostMask32(p.Bits())
		b.v4 = append(b.v4, span4{lo, hi})
		b.v4Prefixes = append(b.v4Prefixes, prefix4{lo, p})
		return
	}
	if addr.Is4In6() {
		// Treat a v4-mapped prefix as the IPv4 prefix it denotes, so it matches
		// the same addresses however the caller spelled it.
		b.AddPrefix(netip.PrefixFrom(addr.Unmap(), p.Bits()-96))
		return
	}
	lo := u128FromBytes(addr.As16())
	hi := lo.or(hostMask128(p.Bits()))
	b.v6 = append(b.v6, span6{lo, hi})
	b.v6Prefixes = append(b.v6Prefixes, prefix6{lo, p})
}

// AddCIDR adds every address in n, for callers holding the older net.IPNet.
func (b *Builder) AddCIDR(n *net.IPNet) {
	if n == nil {
		b.setErr(fmt.Errorf("ipspan: nil *net.IPNet"))
		return
	}
	p, err := netip.ParsePrefix(n.String())
	if err != nil {
		b.setErr(fmt.Errorf("ipspan: %w", err))
		return
	}
	b.AddPrefix(p)
}

// AddPrefixString parses and adds a CIDR block such as "10.0.0.0/8".
func (b *Builder) AddPrefixString(s string) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		b.setErr(fmt.Errorf("ipspan: %w", err))
		return
	}
	b.AddPrefix(p)
}

// AddRange adds every address from lo to hi inclusive. Both must be valid and
// of the same family, and lo must not be above hi.
func (b *Builder) AddRange(lo, hi netip.Addr) {
	lo, hi = lo.Unmap(), hi.Unmap()
	switch {
	case !lo.IsValid() || !hi.IsValid():
		b.setErr(fmt.Errorf("ipspan: invalid address in range %v-%v", lo, hi))
	case lo.Is4() != hi.Is4():
		b.setErr(fmt.Errorf("ipspan: mixed families in range %v-%v", lo, hi))
	case hi.Less(lo):
		b.setErr(fmt.Errorf("ipspan: reversed range %v-%v", lo, hi))
	case lo.Is4():
		b.v4 = append(b.v4, span4{beUint32(lo.As4()), beUint32(hi.As4())})
	default:
		b.v6 = append(b.v6, span6{u128FromBytes(lo.As16()), u128FromBytes(hi.As16())})
	}
}

func (b *Builder) setErr(err error) {
	if b.err == nil {
		b.err = err
	}
}

// BuildSet merges everything added into disjoint spans and indexes them,
// discarding the prefixes. Use BuildTable instead if you need to know which
// prefix an address matched.
//
// It reports the first error seen while adding, if any; on error the returned
// Set is still usable and holds everything that was added successfully, so a
// caller who wants to ignore malformed input may.
func (b *Builder) BuildSet() (*Set, error) {
	s := &Set{
		v4: buildTable4(mergeSpans4(b.v4)),
		v6: buildTable6(mergeSpans6(b.v6)),
	}
	return s, b.err
}

// Contains reports whether addr is in the set. An invalid address is not.
func (s *Set) Contains(addr netip.Addr) bool {
	if s == nil {
		return false
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if addr.Is4() {
		return s.v4.contains(beUint32(addr.As4()))
	}
	if !addr.IsValid() {
		return false
	}
	return s.v6.contains(u128FromBytes(addr.As16()))
}

// ContainsIP reports whether ip is in the set, for callers holding the older
// net.IP.
func (s *Set) ContainsIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	return s.Contains(addr)
}

// ContainsString parses s and reports whether it is in the set. An address
// that does not parse is not in the set.
func (s *Set) ContainsString(str string) bool {
	addr, err := netip.ParseAddr(str)
	if err != nil {
		return false
	}
	return s.Contains(addr)
}

// Spans reports how many disjoint spans the set holds per family, which is the
// true size of the problem after merging. Comparing it with the number of
// blocks added says how much the input collapsed.
func (s *Set) Spans() (v4, v6 int) {
	if s == nil {
		return 0, 0
	}
	return len(s.v4.spans), len(s.v6.spans)
}

// WorstScan reports the most spans a single lookup can compare, per family.
//
// The index narrows a lookup to a short run of spans, and this is the longest
// such run. It is normally one or two; a large value means the spans cluster
// under one index slot, which is the one shape this structure handles poorly.
func (s *Set) WorstScan() (v4, v6 int) {
	if s == nil {
		return 0, 0
	}
	return s.v4.worstScan(), s.v6.worstScan()
}

// sortSpans4 and the merge below are the whole of the collapse: sort by start,
// then join anything overlapping or merely adjacent, since for membership
// [a,b] and [b+1,c] are indistinguishable from [a,c].
func mergeSpans4(in []span4) []span4 {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].lo < in[j].lo })
	out := in[:1]
	for _, s := range in[1:] {
		cur := &out[len(out)-1]
		if cur.hi == ^uint32(0) || s.lo <= cur.hi+1 {
			if s.hi > cur.hi {
				cur.hi = s.hi
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

func mergeSpans6(in []span6) []span6 {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].lo.less(in[j].lo) })
	out := in[:1]
	for _, s := range in[1:] {
		cur := &out[len(out)-1]
		next, ok := cur.hi.next()
		if !ok || !next.less(s.lo) {
			if cur.hi.less(s.hi) {
				cur.hi = s.hi
			}
			continue
		}
		out = append(out, s)
	}
	return out
}
