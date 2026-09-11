package ipspan

import "encoding/binary"

// u128 is a 128-bit address as two words, big-endian: hi holds the leading 64
// bits. Comparing two of them is two integer comparisons, which is what makes
// an IPv6 span check as cheap as an IPv4 one.
type u128 struct{ hi, lo uint64 }

func u128FromBytes(b [16]byte) u128 {
	return u128{
		hi: binary.BigEndian.Uint64(b[0:8]),
		lo: binary.BigEndian.Uint64(b[8:16]),
	}
}

func (a u128) less(b u128) bool {
	return a.hi < b.hi || (a.hi == b.hi && a.lo < b.lo)
}

func (a u128) or(b u128) u128 { return u128{a.hi | b.hi, a.lo | b.lo} }

// next returns a+1, reporting false if a is the last address and adding one
// would wrap.
func (a u128) next() (u128, bool) {
	if a.lo != ^uint64(0) {
		return u128{a.hi, a.lo + 1}, true
	}
	if a.hi == ^uint64(0) {
		return a, false
	}
	return u128{a.hi + 1, 0}, true
}

func beUint32(b [4]byte) uint32 { return binary.BigEndian.Uint32(b[:]) }

// hostMask32 returns the bits a /bits prefix leaves free. Go defines a shift of
// 32 or more on a uint32 as producing zero, so /0 yields all ones without a
// special case.
func hostMask32(bits int) uint32 {
	if bits <= 0 {
		return ^uint32(0)
	}
	if bits >= 32 {
		return 0
	}
	return ^uint32(0) >> uint(bits)
}

func hostMask128(bits int) u128 {
	switch {
	case bits <= 0:
		return u128{^uint64(0), ^uint64(0)}
	case bits >= 128:
		return u128{}
	case bits == 64:
		return u128{0, ^uint64(0)}
	case bits < 64:
		return u128{^uint64(0) >> uint(bits), ^uint64(0)}
	default:
		return u128{0, ^uint64(0) >> uint(bits-64)}
	}
}

// prev returns a-1, reporting false if a is zero and subtracting would wrap.
func (a u128) prev() (u128, bool) {
	if a.lo != 0 {
		return u128{a.hi, a.lo - 1}, true
	}
	if a.hi == 0 {
		return a, false
	}
	return u128{a.hi - 1, ^uint64(0)}, true
}
