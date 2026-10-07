package recon

import (
	"sort"

	"github.com/miketheprogrammer/merkle-x/changelog"
	"github.com/miketheprogrammer/merkle-x/crd"
	"github.com/miketheprogrammer/merkle-x/graph"
)

// ---------------------------------------------------------------------------
// X side: Z downloads X's daily snapshot and patches the X tree with the
// difference from yesterday, so it never rehashes the full dataset again.
// ---------------------------------------------------------------------------

// SnapshotStats summarizes one X-side patch.
type SnapshotStats struct{ Upserts, Deletes, Unchanged int }

// ApplySnapshot diffs today's projected leaves against the X leaf index and
// applies only the differences, at version {asOf, 0}.
func ApplySnapshot(s *State, leaves []crd.Leaf, asOf int64) SnapshotStats {
	var st SnapshotStats
	present := map[crd.Key]bool{}
	for _, l := range leaves {
		present[l.Key] = true
		if e := s.Index[l.Key]; e != nil && !e.Deleted {
			h := l.Hash()
			if e.Hash.Equal(&h) {
				st.Unchanged++
				continue
			}
		}
		s.Put(l, Version{asOf, 0}, "")
		st.Upserts++
	}
	gone := map[crd.Key]bool{}
	for k, e := range s.Index {
		if !e.Deleted && !present[k] {
			gone[k] = true
		}
	}
	for _, k := range sortedKeys(gone) {
		s.Delete(k, Version{asOf, 0}, "")
		st.Deletes++
	}
	return st
}

// ---------------------------------------------------------------------------
// Y side: Z bootstraps from a neptune-export, then consumes the changelog.
// ---------------------------------------------------------------------------

// Consumer maintains the Y-side progressive state from the changelog.
type Consumer struct {
	State   *State
	persons map[string]string       // Y member vertex ID -> canonical person ID
	targets map[string]graph.Target // Y permanent vertex ID -> canonical target
	pending []changelog.Event

	Applied, Stale, NoChange int
	Unresolved               []changelog.Event // edges whose endpoints Z has never seen
}

// Bootstrap builds the baseline from an export taken at T0, before sync is
// turned on. The alias tables ship with the tree as part of the leaf index.
func Bootstrap(n int, export *graph.Store, t0 int64) *Consumer {
	persons, targets := graph.TargetsOf(export)
	return &Consumer{
		State:   Build(n, graph.Project(export), Version{t0, 0}),
		persons: persons,
		targets: targets,
	}
}

// PersonID implements graph.Resolver from Z's alias table.
func (c *Consumer) PersonID(id string) (string, bool) { p, ok := c.persons[id]; return p, ok }

// Target implements graph.Resolver from Z's alias table.
func (c *Consumer) Target(id string) (graph.Target, bool) { t, ok := c.targets[id]; return t, ok }

// Receive buffers delivered events. Nothing is applied until Advance.
func (c *Consumer) Receive(evs []changelog.Event) { c.pending = append(c.pending, evs...) }

// Advance applies every buffered event with SourceTS <= t, in (TS, Seq)
// order, and holds the rest for a later checkpoint. Call it only once the
// ledger's low watermark is past t.
func (c *Consumer) Advance(t int64) {
	var now, later []changelog.Event
	for _, ev := range c.pending {
		if ev.SourceTS <= t {
			now = append(now, ev)
		} else {
			later = append(later, ev)
		}
	}
	sort.Slice(now, func(i, j int) bool {
		if now[i].SourceTS != now[j].SourceTS {
			return now[i].SourceTS < now[j].SourceTS
		}
		return now[i].Seq < now[j].Seq
	})
	for _, ev := range now {
		c.apply(ev)
	}
	c.pending = later
}

func (c *Consumer) tally(o Outcome) {
	switch o {
	case Applied:
		c.Applied++
	case Stale:
		c.Stale++
	case NoChange:
		c.NoChange++
	}
}

func (c *Consumer) apply(ev changelog.Event) {
	v := Version{ev.SourceTS, ev.Seq}
	switch ev.Op {
	case changelog.UpsertVertex:
		if ev.Vertex.Label == graph.LabelMember {
			l := graph.ProjectMember(ev.Vertex)
			c.persons[ev.Vertex.ID] = l.Key.ID
			c.tally(c.State.Put(l, v, ev.RepairID))
		} else if t, ok := graph.ProjectTarget(ev.Vertex); ok {
			// Permanent vertex: learn its identity; it has no leaf of its own.
			c.targets[ev.Vertex.ID] = t
		}
	case changelog.DeleteVertex:
		if pid, ok := c.persons[ev.Vertex.ID]; ok {
			c.tally(c.State.Delete(crd.Key{Kind: crd.KindPerson, ID: pid}, v, ev.RepairID))
		}
	case changelog.AddEdge:
		if l, ok := graph.ProjectEdge(ev.Edge, c); ok {
			c.tally(c.State.Put(l, v, ev.RepairID))
		} else {
			c.Unresolved = append(c.Unresolved, ev)
		}
	case changelog.RemoveEdge:
		if l, ok := graph.ProjectEdge(ev.Edge, c); ok {
			c.tally(c.State.Delete(l.Key, v, ev.RepairID))
		} else {
			c.Unresolved = append(c.Unresolved, ev)
		}
	}
}
