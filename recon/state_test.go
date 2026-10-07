package recon

import (
	"testing"

	"github.com/miketheprogrammer/merkle-x/crd"
)

func TestVersionGatingAndTombstones(t *testing.T) {
	s := NewState(16)
	l, _ := crd.PhoneEdge("p1", "555-010-2030")

	if s.Put(l, Version{10, 1}, "") != Applied {
		t.Fatal("first put must apply")
	}
	if s.Put(l, Version{10, 1}, "") != Stale {
		t.Fatal("duplicate delivery must be ignored")
	}
	if s.Delete(l.Key, Version{11, 5}, "") != Applied {
		t.Fatal("newer delete must apply")
	}
	// A late, older add must not resurrect the deleted edge.
	if s.Put(l, Version{10, 7}, "") != Stale {
		t.Fatal("tombstone must block an older add")
	}
	if b := s.Tree.Bucket(crd.Bucket("p1", 16)); !b.IsZero() {
		t.Fatal("bucket must be empty after delete")
	}
}

func TestOrderIndependence(t *testing.T) {
	a, b := NewState(16), NewState(16)
	p1 := crd.PersonLeaf("p1", map[string]string{"name": "Ada", "tier": "gold"})
	p1b := crd.PersonLeaf("p1", map[string]string{"name": "Ada", "tier": "silver"})
	e, _ := crd.EmailEdge("p1", "Ada@Example.com")

	a.Put(p1, Version{1, 1}, "")
	a.Put(e, Version{1, 2}, "")
	a.Put(p1b, Version{2, 3}, "")

	// Same writes, delivered in a different order.
	b.Put(p1b, Version{2, 3}, "")
	b.Put(e, Version{1, 2}, "")
	b.Put(p1, Version{1, 1}, "") // stale: older than what b already holds

	if a.Root() != b.Root() {
		t.Fatal("delivery order must not change the result")
	}
}
