package lthash

import "testing"

func TestCommutativeAndInvertible(t *testing.T) {
	a, b, c := Of("a"), Of("b"), Of("c")

	var x, y Hash
	x.Add(&a)
	x.Add(&b)
	x.Add(&c)
	y.Add(&c)
	y.Add(&a)
	y.Add(&b)
	if !x.Equal(&y) {
		t.Fatal("addition must be order independent")
	}

	x.Sub(&b)
	var z Hash
	z.Add(&a)
	z.Add(&c)
	if !x.Equal(&z) {
		t.Fatal("subtraction must invert addition")
	}

	x.Sub(&a)
	x.Sub(&c)
	if !x.IsZero() {
		t.Fatal("removing everything must give the empty-set hash")
	}
}

func TestDoubleApplyDoesNotCancel(t *testing.T) {
	a := Of("a")
	var x Hash
	x.Add(&a)
	x.Add(&a)
	if x.IsZero() || x.Equal(&a) {
		t.Fatal("a duplicate add must be visible, unlike XOR")
	}
}

func TestLengthPrefixing(t *testing.T) {
	p, q := Of("ab", "c"), Of("a", "bc")
	if p.Equal(&q) {
		t.Fatal("parts must be length-prefixed")
	}
}
