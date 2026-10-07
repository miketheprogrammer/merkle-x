// Package crd defines the canonical record definitions (CRDs) that both
// sides of the reconciliation project into. Nothing in here knows about the
// source representation (X) or the graph representation (Y); the projectors
// in package source and package graph map their own shapes onto these.
//
// The invented data model:
//
//	Person            core vertex, MUTABLE properties (name, status, tier, country)
//	PhoneNumber       permanent vertex, immutable value (E.164)
//	EmailAddress      permanent vertex, immutable value (lowercased address)
//	Person -has_phone-> PhoneNumber
//	Person -has_email-> EmailAddress
//
// Leaf design:
//
//   - A Person is one leaf. Its hash is the sum of one atom hash per
//     property, so a single property change is "subtract one atom, add one
//     atom" and a mismatch can be localized to the property.
//   - Permanent vertices get NO leaf of their own. Their canonical ID is
//     derived from their value (content addressing), so the value is fully
//     determined by the ID. The only thing about them that can change is
//     their relationship to a Person, which is the edge leaf. A corrupted
//     phone value in Y re-derives to a different ID, so it still surfaces
//     as an edge mismatch. This avoids reference counting and the
//     "orphaned vertex" question entirely.
//   - Every leaf has a Root (the Person ID) that decides its bucket, so all
//     of a person's leaves land in the same bucket and the tree localizes a
//     discrepancy to a person, while updates never need the whole person.
package crd

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/miketheprogrammer/merkle-x/lthash"
)

// Kind is the leaf type.
type Kind string

const (
	KindPerson Kind = "person"
	KindEdge   Kind = "edge"
)

// Canonical edge labels.
const (
	LabelHasPhone = "has_phone"
	LabelHasEmail = "has_email"
)

// Key identifies a leaf.
type Key struct {
	Kind Kind
	ID   string
}

func (k Key) String() string { return string(k.Kind) + ":" + k.ID }

// Atom is one canonical (name, value) pair inside a leaf.
type Atom struct {
	Name  string
	Value string
}

// Leaf is one canonical record.
type Leaf struct {
	Key   Key
	Root  string // bucketing key: always the Person ID
	Atoms []Atom // order is irrelevant to the hash; kept sorted for display
}

// AtomHash hashes one atom, bound to its leaf identity so atoms cannot be
// swapped between leaves without changing the result.
func AtomHash(k Key, a Atom) lthash.Hash {
	return lthash.Of(string(k.Kind), k.ID, a.Name, a.Value)
}

// Hash is the sum of the leaf's atom hashes.
func (l Leaf) Hash() lthash.Hash {
	var h lthash.Hash
	for _, a := range l.Atoms {
		ah := AtomHash(l.Key, a)
		h.Add(&ah)
	}
	return h
}

// Bucket maps a root to one of n buckets (n must be a power of two).
// The tree shape depends only on n, never on the data.
func Bucket(root string, n int) int {
	s := sha256.Sum256([]byte(root))
	return int(binary.BigEndian.Uint64(s[:8]) & uint64(n-1))
}

// ---------------------------------------------------------------------------
// Canonical normalization and identity. Both projectors call these; the
// canonical library is owned by the recon team (Z), not by the loader (Y).
// ---------------------------------------------------------------------------

// NormalizePhone returns "+<digits>", assuming US country code for bare
// 10-digit numbers. Returns "" if nothing usable remains.
func NormalizePhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) == 10 {
		d = "1" + d
	}
	if d == "" {
		return ""
	}
	return "+" + d
}

// NormalizeEmail lowercases and trims.
func NormalizeEmail(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

func contentID(prefix, normalized string) string {
	s := sha256.Sum256([]byte(prefix + "|" + normalized))
	return prefix + "_" + hex.EncodeToString(s[:10])
}

// PhoneID is the content-addressed canonical ID of a phone vertex.
func PhoneID(normalized string) string { return contentID("ph", normalized) }

// EmailID is the content-addressed canonical ID of an email vertex.
func EmailID(normalized string) string { return contentID("em", normalized) }

// ---------------------------------------------------------------------------
// Leaf constructors.
// ---------------------------------------------------------------------------

// PersonProps is the canonical, closed set of mutable Person properties.
var PersonProps = []string{"name", "status", "tier", "country"}

// PersonLeaf builds the canonical Person leaf. Only properties in
// PersonProps are included; anything derived from children (e.g. a
// "primary phone" denormalized onto the person) must never appear here,
// or a child change would force a person rehash.
func PersonLeaf(personID string, props map[string]string) Leaf {
	atoms := make([]Atom, 0, len(PersonProps))
	for _, name := range PersonProps {
		if v, ok := props[name]; ok && v != "" {
			atoms = append(atoms, Atom{name, v})
		}
	}
	return Leaf{Key: Key{KindPerson, personID}, Root: personID, Atoms: atoms}
}

// EdgeLeaf builds the canonical leaf for Person -label-> permanent vertex.
// The target's canonical value is included so a localized mismatch is
// readable; it is redundant with the content-addressed target ID.
func EdgeLeaf(personID, label, targetID, targetValue string) Leaf {
	id := personID + "|" + label + "|" + targetID
	return Leaf{
		Key:  Key{KindEdge, id},
		Root: personID,
		Atoms: []Atom{
			{"from", personID},
			{"label", label},
			{"to", targetID},
			{"value", targetValue},
		},
	}
}

// PhoneEdge is a convenience wrapper that normalizes and derives the ID.
func PhoneEdge(personID, raw string) (Leaf, bool) {
	n := NormalizePhone(raw)
	if n == "" {
		return Leaf{}, false
	}
	return EdgeLeaf(personID, LabelHasPhone, PhoneID(n), n), true
}

// EmailEdge is a convenience wrapper that normalizes and derives the ID.
func EmailEdge(personID, raw string) (Leaf, bool) {
	n := NormalizeEmail(raw)
	if n == "" {
		return Leaf{}, false
	}
	return EdgeLeaf(personID, LabelHasEmail, EmailID(n), n), true
}

// AtomDiff is one property-level difference between two versions of a leaf.
type AtomDiff struct {
	Name  string
	Left  string // "" if absent
	Right string // "" if absent
}

// DiffAtoms compares two leaves with the same key at property level.
func DiffAtoms(left, right Leaf) []AtomDiff {
	lm, rm := map[string]string{}, map[string]string{}
	for _, a := range left.Atoms {
		lm[a.Name] = a.Value
	}
	for _, a := range right.Atoms {
		rm[a.Name] = a.Value
	}
	names := map[string]bool{}
	for n := range lm {
		names[n] = true
	}
	for n := range rm {
		names[n] = true
	}
	var out []AtomDiff
	for n := range names {
		if lm[n] != rm[n] {
			out = append(out, AtomDiff{n, lm[n], rm[n]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
