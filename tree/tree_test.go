package tree

import (
	"testing"

	"github.com/miketheprogrammer/merkle-x/lthash"
)

func TestIncrementalEqualsFromScratch(t *testing.T) {
	a, b := New(64), New(64)
	x, y, z := lthash.Of("x"), lthash.Of("y"), lthash.Of("z")

	// a: built progressively with an update in the middle.
	a.Add(3, &x)
	a.Root()
	a.Add(9, &y)
	a.Sub(3, &x)
	a.Add(3, &z)

	// b: built from scratch from the final state.
	b.Add(9, &y)
	b.Add(3, &z)

	ra, _ := a.Root()
	rb, _ := b.Root()
	if ra != rb {
		t.Fatal("progressive tree must equal from-scratch tree")
	}
}

func TestDirtyPathRehashIsLogarithmic(t *testing.T) {
	tr := New(1024)
	tr.Root()
	h := lthash.Of("one change")
	tr.Add(500, &h)
	_, rehashed := tr.Root()
	if rehashed != 10 { // log2(1024)
		t.Fatalf("one dirty bucket should rehash 10 internal nodes, got %d", rehashed)
	}
}

func TestDiffLocalizes(t *testing.T) {
	a, b := New(256), New(256)
	h := lthash.Of("only in a")
	a.Add(17, &h)
	a.Add(200, &h)
	got, compared := Diff(a, b)
	if len(got) != 2 || got[0] != 17 || got[1] != 200 {
		t.Fatalf("expected buckets [17 200], got %v", got)
	}
	if compared > 2*2*9 {
		t.Fatalf("diff should prune matching subtrees, compared %d nodes", compared)
	}
}
