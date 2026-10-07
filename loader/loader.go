// Package loader is Team Y's Glue job plus loader service: it maps X's
// records onto the graph, writes them, and records each write in the
// changelog. It has its OWN X->Y mapping code, separate from the canonical
// projectors, and supports fault injection so the simulator can show which
// failures recon catches and which it cannot.
package loader

import (
	"fmt"
	"sort"
	"strings"

	"github.com/miketheprogrammer/merkle-x/changelog"
	"github.com/miketheprogrammer/merkle-x/graph"
	"github.com/miketheprogrammer/merkle-x/source"
)

// Fault is an injected failure, applied to every write for one member
// during the next Sync.
type Fault int

const (
	None Fault = iota
	// DropLedger: the write lands in Neptune but the ledger never records it.
	DropLedger
	// DropWrite: the ledger records the write but it never lands in Neptune
	// (the optimistic-ledger blind spot).
	DropWrite
	// TransformBug: the loader encodes the member's status wrong; Neptune and
	// the ledger agree with each other and are both wrong.
	TransformBug
	// Duplicate: every event is delivered twice.
	Duplicate
	// Late: events are delivered one cycle late.
	Late
)

func (f Fault) String() string {
	return [...]string{"none", "drop-ledger", "drop-write", "transform-bug", "duplicate", "late"}[f]
}

// Loader writes X records into the graph store.
type Loader struct {
	Store  *graph.Store
	Log    *changelog.Log
	faults map[string]Fault
	byKey  map[string]string // loader's own permanent-vertex lookup: "phone:15550102030" -> Y ID
	seq    int
}

// New returns a loader writing to store and log.
func New(store *graph.Store, log *changelog.Log) *Loader {
	return &Loader{Store: store, Log: log, faults: map[string]Fault{}, byKey: map[string]string{}}
}

// Inject arms a fault for one member's writes during the next Sync.
func (l *Loader) Inject(memberID string, f Fault) { l.faults[memberID] = f }

// ---------------------------------------------------------------------------
// Y-side encoding (the loader's mapping, intentionally not shared with P_Y).
// ---------------------------------------------------------------------------

var (
	encState = map[string]string{"ACTIVE": "A", "DORMANT": "D", "CLOSED": "C"}
	encTier  = map[string]string{"bronze": "B", "silver": "S", "gold": "G"}
)

func memberVID(id string) string { return "mbr-" + id }

func (l *Loader) memberVertex(m source.Member) graph.Vertex {
	state := encState[m.Status]
	if l.faults[m.MemberID] == TransformBug {
		// The bug: a lookup keyed on the wrong field. Always writes "D".
		state = "D"
		if m.Status == "DORMANT" {
			state = "A"
		}
	}
	return graph.Vertex{
		ID:    memberVID(m.MemberID),
		Label: graph.LabelMember,
		Props: map[string]string{
			"member_ref":   m.MemberID,
			"full_name":    m.DisplayName,
			"state_cd":     state,
			"loyalty_tier": encTier[m.Tier],
			"iso_country":  m.Country,
		},
	}
}

func phoneDigits(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) == 10 {
		d = "1" + d
	}
	return d
}

// ---------------------------------------------------------------------------
// Writes.
// ---------------------------------------------------------------------------

func (l *Loader) write(memberID string, ev changelog.Event, apply func()) {
	switch l.faults[memberID] {
	case DropLedger:
		apply()
	case DropWrite:
		l.Log.Append(ev, 0)
	case Duplicate:
		apply()
		l.Log.AppendDuplicate(l.Log.Append(ev, 0))
	case Late:
		apply()
		l.Log.Append(ev, 1)
	default:
		apply()
		l.Log.Append(ev, 0)
	}
}

// ensureTarget returns the Y ID of the permanent vertex for a phone/email,
// creating it (once, forever) if needed.
func (l *Loader) ensureTarget(memberID, kind, raw string, ts int64, repairID string) string {
	var key string
	var v graph.Vertex
	switch kind {
	case "phone":
		d := phoneDigits(raw)
		key = "phone:" + d
		v = graph.Vertex{Label: graph.LabelPhone, Props: map[string]string{"digits": d}}
	case "email":
		// Stored as received; canonical lowercasing happens only in P_Y.
		key = "email:" + strings.ToLower(strings.TrimSpace(raw))
		v = graph.Vertex{Label: graph.LabelEmail, Props: map[string]string{"addr": strings.TrimSpace(raw)}}
	}
	if id, ok := l.byKey[key]; ok {
		return id
	}
	l.seq++
	v.ID = fmt.Sprintf("%s-%06d", map[string]string{"phone": "pn", "email": "ea"}[kind], l.seq)
	l.byKey[key] = v.ID // optimistic: recorded even if the write is dropped
	vv := v
	l.write(memberID, changelog.Event{SourceTS: ts, Op: changelog.UpsertVertex, Vertex: vv, RepairID: repairID},
		func() { l.Store.V[vv.ID] = vv })
	return v.ID
}

// desiredEdges is the set of edges a member should have in Y.
func (l *Loader) desiredEdges(m source.Member, ts int64, repairID string) map[graph.Edge]bool {
	out := map[graph.Edge]bool{}
	from := memberVID(m.MemberID)
	for _, p := range m.Phones {
		out[graph.Edge{From: from, Label: graph.EdgeHasPhone, To: l.ensureTarget(m.MemberID, "phone", p, ts, repairID)}] = true
	}
	for _, e := range m.Emails {
		out[graph.Edge{From: from, Label: graph.EdgeHasEmail, To: l.ensureTarget(m.MemberID, "email", e, ts, repairID)}] = true
	}
	return out
}

func sortedEdges(set map[graph.Edge]bool) []graph.Edge {
	out := make([]graph.Edge, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].To < out[j].To
	})
	return out
}

func (l *Loader) upsertMember(m source.Member, ts int64, repairID string) {
	v := l.memberVertex(m)
	l.write(m.MemberID, changelog.Event{SourceTS: ts, Op: changelog.UpsertVertex, Vertex: v, RepairID: repairID},
		func() { l.Store.V[v.ID] = v.Clone() })
}

func (l *Loader) addEdge(memberID string, e graph.Edge, ts int64, repairID string) {
	l.write(memberID, changelog.Event{SourceTS: ts, Op: changelog.AddEdge, Edge: e, RepairID: repairID},
		func() { l.Store.E[e] = true })
}

func (l *Loader) removeEdge(memberID string, e graph.Edge, ts int64, repairID string) {
	l.write(memberID, changelog.Event{SourceTS: ts, Op: changelog.RemoveEdge, Edge: e, RepairID: repairID},
		func() { delete(l.Store.E, e) })
}

func (l *Loader) deleteMember(memberID string, ts int64, repairID string) {
	v := graph.Vertex{ID: memberVID(memberID), Label: graph.LabelMember}
	l.write(memberID, changelog.Event{SourceTS: ts, Op: changelog.DeleteVertex, Vertex: v, RepairID: repairID},
		func() { delete(l.Store.V, v.ID) })
}

// Bulk performs the initial ingest. Sync is off, so nothing goes to the
// ledger; Z bootstraps from a neptune-export of the result instead.
func (l *Loader) Bulk(s source.Snapshot) {
	saved := l.Log
	l.Log = changelog.New(0) // discard
	for _, id := range s.IDs() {
		m := s.Members[id]
		l.upsertMember(m, s.AsOf, "")
		for e := range l.desiredEdges(m, s.AsOf, "") {
			l.addEdge(id, e, s.AsOf, "")
		}
	}
	l.Log = saved
}

// Sync applies the day's X changes (prev -> next) to Y, recording each write
// in the ledger with SourceTS = next.AsOf. Armed faults are consumed.
func (l *Loader) Sync(prev, next source.Snapshot) {
	ts := next.AsOf
	for _, id := range next.IDs() {
		m := next.Members[id]
		old, existed := prev.Members[id]
		if !existed || old.DisplayName != m.DisplayName || old.Status != m.Status ||
			old.Tier != m.Tier || old.Country != m.Country || l.faults[id] == TransformBug {
			l.upsertMember(m, ts, "")
		}
		want := l.desiredEdges(m, ts, "")
		had := map[graph.Edge]bool{}
		if existed {
			had = l.desiredEdges(old, ts, "")
		}
		// Removes before adds: if two Y edges ever project to the same
		// canonical edge, the canonical leaf must end up present.
		for _, e := range sortedEdges(had) {
			if !want[e] {
				l.removeEdge(id, e, ts, "")
			}
		}
		for _, e := range sortedEdges(want) {
			if !had[e] {
				l.addEdge(id, e, ts, "")
			}
		}
	}
	for _, id := range prev.IDs() {
		if _, ok := next.Members[id]; ok {
			continue
		}
		for _, e := range sortedEdges(l.desiredEdges(prev.Members[id], ts, "")) {
			l.removeEdge(id, e, ts, "")
		}
		l.deleteMember(id, ts, "")
	}
	l.faults = map[string]Fault{}
}

// Repair re-ships one member from X (the X -> Y leg of the repair loop):
// it compares what Neptune actually holds against the X record and writes
// the difference, tagging every event with repairID. A nil member means the
// member no longer exists in X and must be removed from Y.
func (l *Loader) Repair(memberID string, m *source.Member, ts int64, repairID string) {
	from := memberVID(memberID)
	actual := map[graph.Edge]bool{}
	for e := range l.Store.E {
		if e.From == from {
			actual[e] = true
		}
	}
	if m == nil {
		for _, e := range sortedEdges(actual) {
			l.removeEdge(memberID, e, ts, repairID)
		}
		l.deleteMember(memberID, ts, repairID)
		return
	}
	l.upsertMember(*m, ts, repairID)
	want := l.desiredEdges(*m, ts, repairID)
	for _, e := range sortedEdges(want) {
		if _, ok := l.Store.V[e.To]; !ok {
			// Permanent vertex lost earlier; recreate it from the X value.
			delete(l.byKey, l.keyForVID(e.To))
		}
	}
	want = l.desiredEdges(*m, ts, repairID)
	// Removes first: a dangling edge to a lost phone vertex and its
	// replacement edge to the recreated vertex project to the SAME canonical
	// edge, so the remove must not be the last word.
	for _, e := range sortedEdges(actual) {
		if !want[e] {
			l.removeEdge(memberID, e, ts, repairID)
		}
	}
	for _, e := range sortedEdges(want) {
		if !actual[e] {
			l.addEdge(memberID, e, ts, repairID)
		}
	}
}

func (l *Loader) keyForVID(vid string) string {
	for k, v := range l.byKey {
		if v == vid {
			return k
		}
	}
	return ""
}
