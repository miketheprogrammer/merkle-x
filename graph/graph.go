// Package graph is Team Y's representation: a property graph like the one
// loaded into Neptune. It deliberately differs from X's shape so the two
// projectors have real work to do:
//
//	X Member.DisplayName  -> Y Member.full_name
//	X Member.Status       -> Y Member.state_cd     (ACTIVE=A, DORMANT=D, CLOSED=C)
//	X Member.Tier         -> Y Member.loyalty_tier (bronze=B, silver=S, gold=G)
//	X phone string        -> Y PhoneNumber vertex, digits only, loader-assigned ID
//	X email string        -> Y EmailAddress vertex, stored as received
//
// PhoneNumber and EmailAddress vertices are permanent: once created they
// are never modified or deleted, and are shared by every member that
// references the same value. Only HAS_PHONE / HAS_EMAIL edges come and go.
package graph

import (
	"sort"
	"strings"

	"github.com/miketheprogrammer/merkle-x/crd"
)

// Y vertex labels.
const (
	LabelMember = "Member"
	LabelPhone  = "PhoneNumber"
	LabelEmail  = "EmailAddress"
)

// Y edge labels.
const (
	EdgeHasPhone = "HAS_PHONE"
	EdgeHasEmail = "HAS_EMAIL"
)

// Vertex is a property-graph vertex.
type Vertex struct {
	ID    string
	Label string
	Props map[string]string
}

// Clone deep-copies a vertex.
func (v Vertex) Clone() Vertex {
	p := make(map[string]string, len(v.Props))
	for k, x := range v.Props {
		p[k] = x
	}
	v.Props = p
	return v
}

// Edge is a property-less edge, identified by its endpoints and label.
type Edge struct {
	From  string
	Label string
	To    string
}

// Store is an in-memory stand-in for the Neptune cluster.
type Store struct {
	V map[string]Vertex
	E map[Edge]bool
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{V: map[string]Vertex{}, E: map[Edge]bool{}} }

// Export returns a point-in-time copy, standing in for neptune-export run
// against a cloned cluster.
func (s *Store) Export() *Store {
	out := NewStore()
	for id, v := range s.V {
		out.V[id] = v.Clone()
	}
	for e := range s.E {
		out.E[e] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// P_Y: graph representation -> canonical leaves.
//
// This is written independently of the loader's X->Y mapping. It decodes Y's
// codes back to canonical values and re-derives permanent-vertex identity
// from the stored value, never from the loader's vertex ID. If it reused the
// loader's mapping code, a mapping bug would appear on both sides and cancel.
// ---------------------------------------------------------------------------

var (
	decodeState = map[string]string{"A": "ACTIVE", "D": "DORMANT", "C": "CLOSED"}
	decodeTier  = map[string]string{"B": "bronze", "S": "silver", "G": "gold"}
)

func decode(m map[string]string, code string) string {
	if v, ok := m[code]; ok {
		return v
	}
	return "?" + code // unknown codes surface verbatim in the diff
}

// Target is the canonical identity of a permanent vertex.
type Target struct {
	ID    string // content-addressed canonical ID
	Value string // canonical normalized value
	Label string // canonical edge label that points at it
}

// ProjectMember projects a Member vertex into its canonical Person leaf.
func ProjectMember(v Vertex) crd.Leaf {
	return crd.PersonLeaf(v.Props["member_ref"], map[string]string{
		"name":    v.Props["full_name"],
		"status":  decode(decodeState, v.Props["state_cd"]),
		"tier":    decode(decodeTier, v.Props["loyalty_tier"]),
		"country": v.Props["iso_country"],
	})
}

// ProjectTarget derives the canonical identity of a permanent vertex.
func ProjectTarget(v Vertex) (Target, bool) {
	switch v.Label {
	case LabelPhone:
		n := crd.NormalizePhone(v.Props["digits"])
		return Target{crd.PhoneID(n), n, crd.LabelHasPhone}, n != ""
	case LabelEmail:
		n := crd.NormalizeEmail(v.Props["addr"])
		return Target{crd.EmailID(n), n, crd.LabelHasEmail}, n != ""
	}
	return Target{}, false
}

// Resolver looks up what a Y vertex ID projects to. Z implements this from
// its alias table; an export implements it from the exported vertices.
type Resolver interface {
	PersonID(yID string) (string, bool)
	Target(yID string) (Target, bool)
}

// ProjectEdge projects a Y edge into its canonical edge leaf.
func ProjectEdge(e Edge, r Resolver) (crd.Leaf, bool) {
	pid, ok := r.PersonID(e.From)
	if !ok {
		return crd.Leaf{}, false
	}
	t, ok := r.Target(e.To)
	if !ok {
		return crd.Leaf{}, false
	}
	return crd.EdgeLeaf(pid, t.Label, t.ID, t.Value), true
}

type storeResolver struct{ s *Store }

func (r storeResolver) PersonID(id string) (string, bool) {
	v, ok := r.s.V[id]
	if !ok || v.Label != LabelMember {
		return "", false
	}
	return v.Props["member_ref"], true
}

func (r storeResolver) Target(id string) (Target, bool) {
	v, ok := r.s.V[id]
	if !ok {
		return Target{}, false
	}
	return ProjectTarget(v)
}

// Project is P_Y over a full export. Dangling edges (whose endpoint vertex
// is missing) do not project, so they surface as missing edge leaves.
func Project(s *Store) []crd.Leaf {
	ids := make([]string, 0, len(s.V))
	for id := range s.V {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []crd.Leaf
	for _, id := range ids {
		if v := s.V[id]; v.Label == LabelMember {
			out = append(out, ProjectMember(v))
		}
	}
	edges := make([]Edge, 0, len(s.E))
	for e := range s.E {
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool {
		return strings.Join([]string{edges[i].From, edges[i].Label, edges[i].To}, "|") <
			strings.Join([]string{edges[j].From, edges[j].Label, edges[j].To}, "|")
	})
	seen := map[crd.Key]bool{}
	r := storeResolver{s}
	for _, e := range edges {
		if l, ok := ProjectEdge(e, r); ok && !seen[l.Key] {
			seen[l.Key] = true
			out = append(out, l)
		}
	}
	return out
}

// TargetsOf returns every permanent vertex's projection, keyed by Y ID.
// Shipped alongside the baseline tree as Z's alias table.
func TargetsOf(s *Store) (persons map[string]string, targets map[string]Target) {
	persons, targets = map[string]string{}, map[string]Target{}
	for id, v := range s.V {
		if v.Label == LabelMember {
			persons[id] = v.Props["member_ref"]
		} else if t, ok := ProjectTarget(v); ok {
			targets[id] = t
		}
	}
	return persons, targets
}
