package crd

import "testing"

func TestPersonPropertyChangeIsOneAtomDelta(t *testing.T) {
	before := PersonLeaf("p1", map[string]string{"name": "Ada", "status": "ACTIVE", "tier": "gold", "country": "US"})
	after := PersonLeaf("p1", map[string]string{"name": "Ada", "status": "DORMANT", "tier": "gold", "country": "US"})

	// Patch the old leaf hash with only the changed atom.
	h := before.Hash()
	oldAtom := AtomHash(before.Key, Atom{"status", "ACTIVE"})
	newAtom := AtomHash(before.Key, Atom{"status", "DORMANT"})
	h.Sub(&oldAtom)
	h.Add(&newAtom)

	want := after.Hash()
	if !h.Equal(&want) {
		t.Fatal("property-level patch must equal full recompute")
	}
}

func TestPhoneNormalizationIsRepresentationIndependent(t *testing.T) {
	a, _ := PhoneEdge("p1", "(555) 010-2030")
	b, _ := PhoneEdge("p1", "15550102030")
	if a.Key != b.Key {
		t.Fatalf("same phone in different formats must have one identity: %v vs %v", a.Key, b.Key)
	}
}

func TestBucketIsStable(t *testing.T) {
	if Bucket("p1", 256) != Bucket("p1", 256) {
		t.Fatal("bucket must be deterministic")
	}
	for i := 0; i < 1000; i++ {
		if b := Bucket(string(rune(i)), 64); b < 0 || b >= 64 {
			t.Fatal("bucket out of range")
		}
	}
}
