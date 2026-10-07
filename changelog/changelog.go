// Package changelog is the ledger service between Y and Z. It records what
// the loader says it wrote (optimistic: it does not observe Neptune), and
// delivers events at least once, in arbitrary order, possibly late.
package changelog

import (
	"math"
	"math/rand"

	"github.com/miketheprogrammer/merkle-x/graph"
)

// Op is the kind of write.
type Op string

const (
	UpsertVertex Op = "upsert_vertex" // full after-image of one vertex
	DeleteVertex Op = "delete_vertex"
	AddEdge      Op = "add_edge"
	RemoveEdge   Op = "remove_edge"
)

// Event is one ledger entry.
type Event struct {
	Seq      int64 // ledger sequence, unique per append
	SourceTS int64 // X's version for the change; same domain as Snapshot.AsOf
	Op       Op
	Vertex   graph.Vertex // UpsertVertex / DeleteVertex
	Edge     graph.Edge   // AddEdge / RemoveEdge
	RepairID string       // set when the write is a repair re-ship
}

type pending struct {
	ev    Event
	delay int // delivery cycles to hold back
}

// Log is the ledger plus a delivery simulator.
type Log struct {
	seq     int64
	pending []pending
	rng     *rand.Rand
}

// New returns an empty log.
func New(seed int64) *Log { return &Log{rng: rand.New(rand.NewSource(seed))} }

// Append records an event. delay > 0 holds it back that many deliveries.
func (l *Log) Append(ev Event, delay int) Event {
	l.seq++
	ev.Seq = l.seq
	l.pending = append(l.pending, pending{ev, delay})
	return ev
}

// AppendDuplicate re-sends an already-sequenced event (at-least-once).
func (l *Log) AppendDuplicate(ev Event) { l.pending = append(l.pending, pending{ev, 0}) }

// Deliver returns every event that is due, in shuffled order, and ages
// the rest by one cycle.
func (l *Log) Deliver() []Event {
	var due []Event
	var keep []pending
	for _, p := range l.pending {
		if p.delay <= 0 {
			due = append(due, p.ev)
		} else {
			p.delay--
			keep = append(keep, p)
		}
	}
	l.pending = keep
	l.rng.Shuffle(len(due), func(i, j int) { due[i], due[j] = due[j], due[i] })
	return due
}

// LowWatermark is the smallest SourceTS not yet delivered; every event with
// SourceTS strictly below it has been delivered. A consumer may close the
// checkpoint for T only when LowWatermark() > T.
func (l *Log) LowWatermark() int64 {
	lw := int64(math.MaxInt64)
	for _, p := range l.pending {
		if p.ev.SourceTS < lw {
			lw = p.ev.SourceTS
		}
	}
	return lw
}
