// Package source is Team X's representation: a daily snapshot of nested,
// denormalized member records, the way a data-lake export typically looks.
// Phones and emails are bare strings in whatever format X captured them.
package source

import (
	"fmt"
	"math/rand"
	"sort"

	"github.com/miketheprogrammer/merkle-x/crd"
)

// Member is one X1 record.
type Member struct {
	MemberID    string
	DisplayName string
	Status      string // ACTIVE | DORMANT | CLOSED
	Tier        string // bronze | silver | gold
	Country     string
	Phones      []string // free-form, e.g. "(555) 010-2030"
	Emails      []string // free-form case
}

// Snapshot is X's daily dataset, cut at AsOf (a source version, not a
// wall-clock time). The changelog's SourceTS lives in the same domain.
type Snapshot struct {
	AsOf    int64
	Members map[string]Member
}

// Clone deep-copies a snapshot.
func (s Snapshot) Clone() Snapshot {
	out := Snapshot{AsOf: s.AsOf, Members: make(map[string]Member, len(s.Members))}
	for id, m := range s.Members {
		m.Phones = append([]string(nil), m.Phones...)
		m.Emails = append([]string(nil), m.Emails...)
		out.Members[id] = m
	}
	return out
}

// IDs returns member IDs in sorted order (deterministic iteration).
func (s Snapshot) IDs() []string {
	ids := make([]string, 0, len(s.Members))
	for id := range s.Members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Project is P_X: X's representation -> canonical leaves.
func Project(s Snapshot) []crd.Leaf {
	var out []crd.Leaf
	for _, id := range s.IDs() {
		out = append(out, ProjectMember(s.Members[id])...)
	}
	return out
}

// ProjectMember projects one member into its Person leaf and edge leaves.
func ProjectMember(m Member) []crd.Leaf {
	out := []crd.Leaf{crd.PersonLeaf(m.MemberID, map[string]string{
		"name":    m.DisplayName,
		"status":  m.Status,
		"tier":    m.Tier,
		"country": m.Country,
	})}
	seen := map[crd.Key]bool{}
	for _, p := range m.Phones {
		if l, ok := crd.PhoneEdge(m.MemberID, p); ok && !seen[l.Key] {
			seen[l.Key] = true
			out = append(out, l)
		}
	}
	for _, e := range m.Emails {
		if l, ok := crd.EmailEdge(m.MemberID, e); ok && !seen[l.Key] {
			seen[l.Key] = true
			out = append(out, l)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Synthetic data. Entirely invented.
// ---------------------------------------------------------------------------

var (
	firstNames = []string{"Ada", "Bao", "Chidi", "Dana", "Elif", "Farah", "Goran", "Hana", "Ilya", "Joon", "Kemi", "Luis", "Mira", "Nils", "Odile", "Priya"}
	lastNames  = []string{"Okafor", "Lindqvist", "Moreau", "Tanaka", "Haddad", "Novak", "Reyes", "Iyer", "Castell", "Brandt", "Osei", "Varga"}
	statuses   = []string{"ACTIVE", "DORMANT", "CLOSED"}
	tiers      = []string{"bronze", "silver", "gold"}
	countries  = []string{"US", "CA", "GB", "DE", "IN", "BR"}
)

// Gen produces deterministic synthetic data.
type Gen struct {
	rng       *rand.Rand
	nextID    int
	household []string // shared phones, to exercise many-to-one edges
}

// NewGen seeds a generator.
func NewGen(seed int64) *Gen {
	g := &Gen{rng: rand.New(rand.NewSource(seed))}
	for i := 0; i < 25; i++ {
		g.household = append(g.household, g.phone())
	}
	return g
}

func (g *Gen) pick(xs []string) string { return xs[g.rng.Intn(len(xs))] }

// phone returns a random number in one of several human formats.
func (g *Gen) phone() string {
	a, b, c := 200+g.rng.Intn(700), g.rng.Intn(1000), g.rng.Intn(10000)
	switch g.rng.Intn(3) {
	case 0:
		return fmt.Sprintf("(%03d) %03d-%04d", a, b, c)
	case 1:
		return fmt.Sprintf("+1 %03d.%03d.%04d", a, b, c)
	default:
		return fmt.Sprintf("%03d%03d%04d", a, b, c)
	}
}

func (g *Gen) email(name string) string {
	domains := []string{"Example.com", "mail.test", "Inbox.Example"}
	return fmt.Sprintf("%s.%d@%s", name, g.rng.Intn(10000), g.pick(domains))
}

// NewMember makes a fresh member.
func (g *Gen) NewMember() Member {
	g.nextID++
	first := g.pick(firstNames)
	m := Member{
		MemberID:    fmt.Sprintf("M%06d", g.nextID),
		DisplayName: first + " " + g.pick(lastNames),
		Status:      g.pick(statuses),
		Tier:        g.pick(tiers),
		Country:     g.pick(countries),
	}
	for i := 0; i < 1+g.rng.Intn(2); i++ {
		m.Phones = append(m.Phones, g.phone())
	}
	if g.rng.Intn(4) == 0 {
		m.Phones = append(m.Phones, g.pick(g.household))
	}
	for i := 0; i < 1+g.rng.Intn(2); i++ {
		m.Emails = append(m.Emails, g.email(first))
	}
	return m
}

// Initial builds the first snapshot.
func (g *Gen) Initial(n int, asOf int64) Snapshot {
	s := Snapshot{AsOf: asOf, Members: map[string]Member{}}
	for i := 0; i < n; i++ {
		m := g.NewMember()
		s.Members[m.MemberID] = m
	}
	return s
}

// Evolve returns the next day's snapshot with roughly `changes` random
// mutations: property edits, phone/email adds and removals, new members,
// and deletions.
func (g *Gen) Evolve(prev Snapshot, asOf int64, changes int) Snapshot {
	s := prev.Clone()
	s.AsOf = asOf
	ids := s.IDs()
	for i := 0; i < changes; i++ {
		id := ids[g.rng.Intn(len(ids))]
		m, ok := s.Members[id]
		if !ok {
			continue // deleted earlier today
		}
		switch g.rng.Intn(10) {
		case 0, 1, 2:
			m.Tier = g.pick(tiers)
		case 3:
			m.Status = g.pick(statuses)
		case 4:
			m.DisplayName = g.pick(firstNames) + " " + g.pick(lastNames)
		case 5:
			m.Phones = append(m.Phones, g.phone())
		case 6:
			if len(m.Phones) > 1 {
				m.Phones = m.Phones[1:]
			}
		case 7:
			m.Emails = append(m.Emails, g.email("x"))
		case 8:
			nm := g.NewMember()
			s.Members[nm.MemberID] = nm
			continue
		case 9:
			delete(s.Members, id)
			continue
		}
		s.Members[id] = m
	}
	return s
}

// Pick returns a deterministic existing member ID, for fault targeting.
func (g *Gen) Pick(s Snapshot) string {
	ids := s.IDs()
	return ids[g.rng.Intn(len(ids))]
}

// NewPhone exposes the phone generator to the simulator.
func (g *Gen) NewPhone() string { return g.phone() }
