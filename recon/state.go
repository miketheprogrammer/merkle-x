// Package recon holds Team Z's progressive Merkle state and the comparison
// logic. One State type serves both sides: the X side is patched from daily
// snapshot diffs, the Y side from the changelog.
package recon

import (
	"sort"

	"github.com/miketheprogrammer/merkle-x/crd"
	"github.com/miketheprogrammer/merkle-x/lthash"
	"github.com/miketheprogrammer/merkle-x/tree"
)

// Version orders writes to one leaf: source version first, ledger sequence
// as tie-break. A write is applied only if it is newer than what the leaf
// index holds, which makes duplicates and reordering harmless.
type Version struct {
	TS  int64
	Seq int64
}

// Less reports v < o.
func (v Version) Less(o Version) bool {
	if v.TS != o.TS {
		return v.TS < o.TS
	}
	return v.Seq < o.Seq
}

// Entry is one row of the leaf index.
//
// In production this row is (key, version, 32-byte digest, bucket) in
// DynamoDB or partitioned Parquet; the full Leaf is kept here only so the
// simulator can print property-level diffs without a second lookup.
type Entry struct {
	Version Version
	Hash    lthash.Hash
	Bucket  int
	Leaf    crd.Leaf
	Deleted bool // tombstone: blocks an older, late "add" from resurrecting it
}

// Outcome of applying one write.
type Outcome int

const (
	Applied  Outcome = iota
	Stale            // older than or equal to the indexed version; ignored
	NoChange         // newer, but the content hash is identical
)

// State is a progressive tree plus its leaf index.
type State struct {
	Tree    *tree.Tree
	Index   map[crd.Key]*Entry
	delta   map[int]*lthash.Hash // per-bucket change since the last checkpoint
	repairs map[int][]string     // buckets touched by repair writes since the last checkpoint
}

// NewState returns an empty state with n buckets.
func NewState(n int) *State {
	return &State{Tree: tree.New(n), Index: map[crd.Key]*Entry{}, delta: map[int]*lthash.Hash{}, repairs: map[int][]string{}}
}

func (s *State) bump(b int, add, sub *lthash.Hash) {
	d := s.delta[b]
	if d == nil {
		d = new(lthash.Hash)
		s.delta[b] = d
	}
	if add != nil {
		d.Add(add)
		s.Tree.Add(b, add)
	}
	if sub != nil {
		d.Sub(sub)
		s.Tree.Sub(b, sub)
	}
}

// Put inserts or replaces a leaf if v is newer than the indexed version.
func (s *State) Put(l crd.Leaf, v Version, repairID string) Outcome {
	e := s.Index[l.Key]
	if e != nil && !e.Version.Less(v) {
		return Stale
	}
	h := l.Hash()
	b := crd.Bucket(l.Root, s.Tree.Buckets())
	if e != nil && !e.Deleted && e.Hash.Equal(&h) && e.Bucket == b {
		e.Version = v
		return NoChange
	}
	if e != nil && !e.Deleted {
		s.bump(e.Bucket, nil, &e.Hash)
	}
	s.bump(b, &h, nil)
	s.Index[l.Key] = &Entry{Version: v, Hash: h, Bucket: b, Leaf: l}
	if repairID != "" {
		s.repairs[b] = append(s.repairs[b], repairID)
	}
	return Applied
}

// Delete removes a leaf if v is newer than the indexed version, leaving a
// tombstone.
func (s *State) Delete(k crd.Key, v Version, repairID string) Outcome {
	e := s.Index[k]
	if e != nil && !e.Version.Less(v) {
		return Stale
	}
	if e == nil || e.Deleted {
		s.Index[k] = &Entry{Version: v, Deleted: true}
		return NoChange
	}
	s.bump(e.Bucket, nil, &e.Hash)
	if repairID != "" {
		s.repairs[e.Bucket] = append(s.repairs[e.Bucket], repairID)
	}
	e.Version, e.Deleted, e.Hash = v, true, lthash.Hash{}
	return Applied
}

// Checkpoint closes the current increment: it returns the per-bucket deltas
// and repair tags accumulated since the previous checkpoint, then resets them.
func (s *State) Checkpoint() (delta map[int]lthash.Hash, repairs map[int][]string) {
	delta = map[int]lthash.Hash{}
	for b, d := range s.delta {
		if !d.IsZero() {
			delta[b] = *d
		}
	}
	repairs = s.repairs
	s.delta, s.repairs = map[int]*lthash.Hash{}, map[int][]string{}
	return delta, repairs
}

// Root returns the current root digest.
func (s *State) Root() [32]byte { r, _ := s.Tree.Root(); return r }

// LiveLeaves returns the non-deleted leaves in one bucket, sorted by key.
// Production would read a bucket-partitioned index instead of scanning.
func (s *State) LiveLeaves(bucket int) map[crd.Key]*Entry {
	out := map[crd.Key]*Entry{}
	for k, e := range s.Index {
		if !e.Deleted && e.Bucket == bucket {
			out[k] = e
		}
	}
	return out
}

// Build loads leaves as a baseline at version v.
func Build(n int, leaves []crd.Leaf, v Version) *State {
	s := NewState(n)
	for _, l := range leaves {
		s.Put(l, v, "")
	}
	s.Checkpoint() // a baseline is not an increment
	return s
}

func sortedKeys(m map[crd.Key]bool) []crd.Key {
	out := make([]crd.Key, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
