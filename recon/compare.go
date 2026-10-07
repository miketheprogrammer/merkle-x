package recon

import (
	"sort"

	"github.com/miketheprogrammer/merkle-x/crd"
	"github.com/miketheprogrammer/merkle-x/lthash"
	"github.com/miketheprogrammer/merkle-x/tree"
)

// LeafDiff is one localized discrepancy.
type LeafDiff struct {
	Key   crd.Key
	Kind  string // "missing-in-Y" | "extra-in-Y" | "differs"
	Atoms []crd.AtomDiff
}

// BucketReport is all discrepancies in one bucket.
type BucketReport struct {
	Bucket int
	Leaves []LeafDiff
}

// DiffBucket compares two states inside one bucket at leaf and property level.
func DiffBucket(x, y *State, b int) []LeafDiff {
	xl, yl := x.LiveLeaves(b), y.LiveLeaves(b)
	keys := map[crd.Key]bool{}
	for k := range xl {
		keys[k] = true
	}
	for k := range yl {
		keys[k] = true
	}
	var out []LeafDiff
	for _, k := range sortedKeys(keys) {
		xe, ye := xl[k], yl[k]
		switch {
		case ye == nil:
			out = append(out, LeafDiff{k, "missing-in-Y", nil})
		case xe == nil:
			out = append(out, LeafDiff{k, "extra-in-Y", nil})
		case !xe.Hash.Equal(&ye.Hash):
			out = append(out, LeafDiff{k, "differs", crd.DiffAtoms(xe.Leaf, ye.Leaf)})
		}
	}
	return out
}

// Cumulative compares the full trees: root first, then BFS down to the
// differing buckets, then leaf and property level inside them.
func Cumulative(x, y *State) (reports []BucketReport, compared int) {
	buckets, compared := tree.Diff(x.Tree, y.Tree)
	for _, b := range buckets {
		reports = append(reports, BucketReport{b, DiffBucket(x, y, b)})
	}
	return reports, compared
}

// Increment compares one day's per-bucket deltas from each side and returns
// the buckets whose change today does not match. This isolates new drift
// from anything carried over from earlier days.
func Increment(dx, dy map[int]lthash.Hash) []int {
	seen := map[int]bool{}
	for b := range dx {
		seen[b] = true
	}
	for b := range dy {
		seen[b] = true
	}
	var out []int
	for b := range seen {
		x, y := dx[b], dy[b]
		if !x.Equal(&y) {
			out = append(out, b)
		}
	}
	sort.Ints(out)
	return out
}
