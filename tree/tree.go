// Package tree is a fixed-shape Merkle tree over LtHash bucket accumulators.
//
// The shape depends only on the bucket count, never on the data, so two
// trees built on different machines from different representations are
// directly comparable node by node. Bucket accumulators are patched in O(1)
// per change; internal digests are recomputed lazily, only along dirty paths,
// when Root is called (i.e. at checkpoint time, not per event).
package tree

import (
	"crypto/sha256"

	"github.com/miketheprogrammer/merkle-x/lthash"
)

// Tree is a complete binary tree in heap layout: node 1 is the root, node i
// has children 2i and 2i+1, and leaves (buckets) occupy n..2n-1.
type Tree struct {
	n     int
	acc   []lthash.Hash // bucket accumulators, len n
	nodes [][32]byte    // node digests, len 2n
	dirty map[int]bool  // dirty bucket indices
}

// New returns an empty tree with n buckets; n must be a power of two.
func New(n int) *Tree {
	if n <= 0 || n&(n-1) != 0 {
		panic("tree: bucket count must be a power of two")
	}
	t := &Tree{n: n, acc: make([]lthash.Hash, n), nodes: make([][32]byte, 2*n), dirty: map[int]bool{}}
	for b := 0; b < n; b++ {
		t.dirty[b] = true
	}
	return t
}

// Buckets returns the bucket count.
func (t *Tree) Buckets() int { return t.n }

// Add folds h into bucket b.
func (t *Tree) Add(b int, h *lthash.Hash) { t.acc[b].Add(h); t.dirty[b] = true }

// Sub removes h from bucket b.
func (t *Tree) Sub(b int, h *lthash.Hash) { t.acc[b].Sub(h); t.dirty[b] = true }

// Bucket returns a copy of bucket b's accumulator.
func (t *Tree) Bucket(b int) lthash.Hash { return t.acc[b] }

func leafDigest(h *lthash.Hash) [32]byte {
	x := sha256.New()
	x.Write([]byte{0})
	x.Write(h.Bytes())
	var d [32]byte
	copy(d[:], x.Sum(nil))
	return d
}

func nodeDigest(l, r [32]byte) [32]byte {
	var buf [65]byte
	buf[0] = 1
	copy(buf[1:], l[:])
	copy(buf[33:], r[:])
	return sha256.Sum256(buf[:])
}

// Root recomputes only the dirty paths and returns the root digest.
// It returns how many internal nodes were rehashed, for illustration.
func (t *Tree) Root() (root [32]byte, rehashed int) {
	parents := map[int]bool{}
	for b := range t.dirty {
		i := t.n + b
		t.nodes[i] = leafDigest(&t.acc[b])
		parents[i/2] = true
	}
	t.dirty = map[int]bool{}
	for len(parents) > 0 {
		next := map[int]bool{}
		for p := range parents {
			if p < 1 {
				continue
			}
			t.nodes[p] = nodeDigest(t.nodes[2*p], t.nodes[2*p+1])
			rehashed++
			if p > 1 {
				next[p/2] = true
			}
		}
		parents = next
	}
	return t.nodes[1], rehashed
}

// Diff walks both trees top-down (BFS) and returns the buckets whose
// digests differ, plus the number of node comparisons it took. Matching
// subtrees are pruned immediately, so the cost is proportional to the
// number of differing buckets times the tree height, not to n.
func Diff(a, b *Tree) (buckets []int, compared int) {
	if a.n != b.n {
		panic("tree: cannot diff trees of different shape")
	}
	a.Root()
	b.Root()
	queue := []int{1}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		compared++
		if a.nodes[i] == b.nodes[i] {
			continue
		}
		if i >= a.n {
			buckets = append(buckets, i-a.n)
			continue
		}
		queue = append(queue, 2*i, 2*i+1)
	}
	return buckets, compared
}
