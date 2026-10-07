// Package lthash implements LtHash16, a homomorphic set/multiset hash.
//
// A Hash is a vector of 1024 uint16 lanes. Combining is lane-wise addition
// mod 2^16, so it is commutative, associative, and invertible:
//
//	Of(a) + Of(b) == Of(b) + Of(a)
//	(X + Of(a)) - Of(a) == X
//
// That is exactly what a progressive Merkle bucket needs: inserts add,
// deletes subtract, updates subtract-old/add-new, and the order of
// application does not matter. Unlike XOR, applying the same element twice
// does NOT cancel out, so an accidental double-apply shows up as drift
// instead of hiding.
//
// Element hashes are expanded with SHAKE128 to 2048 bytes. This mirrors the
// LtHash construction described by Bellare-Micciancio and used at Meta for
// incremental database integrity checking.
package lthash

import (
	"crypto/sha256"
	"crypto/sha3"
	"encoding/binary"
	"encoding/hex"
)

// Lanes is the number of 16-bit lanes (2048 bytes of state).
const Lanes = 1024

// Hash is an LtHash16 accumulator. The zero value is the hash of the empty set.
type Hash [Lanes]uint16

// Of hashes one element. Parts are length-prefixed so that ("ab","c") and
// ("a","bc") hash differently.
func Of(parts ...string) Hash {
	x := sha3.NewSHAKE128()
	var lenBuf [8]byte
	x.Write([]byte("merkle-x/lthash16/v1"))
	for _, p := range parts {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(p)))
		x.Write(lenBuf[:])
		x.Write([]byte(p))
	}
	var buf [Lanes * 2]byte
	x.Read(buf[:])
	var h Hash
	for i := range h {
		h[i] = binary.LittleEndian.Uint16(buf[2*i:])
	}
	return h
}

// Add sets h = h + o.
func (h *Hash) Add(o *Hash) {
	for i := range h {
		h[i] += o[i]
	}
}

// Sub sets h = h - o.
func (h *Hash) Sub(o *Hash) {
	for i := range h {
		h[i] -= o[i]
	}
}

// IsZero reports whether h is the hash of the empty set.
func (h *Hash) IsZero() bool {
	for _, v := range h {
		if v != 0 {
			return false
		}
	}
	return true
}

// Equal reports lane-wise equality.
func (h *Hash) Equal(o *Hash) bool { return *h == *o }

// Bytes returns the little-endian serialization of the lanes.
func (h *Hash) Bytes() []byte {
	out := make([]byte, Lanes*2)
	for i, v := range h {
		binary.LittleEndian.PutUint16(out[2*i:], v)
	}
	return out
}

// Digest compresses the accumulator to 32 bytes for use as a Merkle leaf.
func (h *Hash) Digest() [32]byte { return sha256.Sum256(h.Bytes()) }

// Short returns a short hex prefix of the digest, for logs.
func (h *Hash) Short() string {
	d := h.Digest()
	return hex.EncodeToString(d[:6])
}
