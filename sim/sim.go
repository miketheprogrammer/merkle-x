// Package sim runs the end-to-end scenario: X produces daily snapshots, Y
// loads them into a graph and writes a changelog, Z keeps progressive trees
// for both sides, compares daily increments, routes repairs, and runs a
// periodic ground-truth check against a fresh export.
package sim

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/miketheprogrammer/merkle-x/changelog"
	"github.com/miketheprogrammer/merkle-x/crd"
	"github.com/miketheprogrammer/merkle-x/graph"
	"github.com/miketheprogrammer/merkle-x/loader"
	"github.com/miketheprogrammer/merkle-x/recon"
	"github.com/miketheprogrammer/merkle-x/source"
)

// Config sizes the run.
type Config struct {
	Members        int
	Buckets        int // power of two
	ChangesPerDay  int
	Seed           int64
	GroundTruthDay int // day on which Z re-exports Neptune and compares
}

// DefaultConfig is what cmd/sim runs.
var DefaultConfig = Config{Members: 2000, Buckets: 256, ChangesPerDay: 60, Seed: 42, GroundTruthDay: 5}

// Day is what happened on one simulated day, for tests and printing.
type Day struct {
	N               int
	Injected        map[string]loader.Fault
	Deferred        bool            // checkpoint waited for late events
	NewDrift        map[string]bool // person IDs with unexplained drift today
	RepairsClosed   map[string]bool // repair IDs confirmed closed today
	RepairsOpen     map[string]bool // repair IDs whose bucket still differs
	CumulativeClean bool            // X tree root == Z tree root
	GroundTruth     map[string]bool // person IDs where Neptune != ledger-derived Z
	Stale           int             // duplicate/out-of-date events ignored today
}

// Result is the whole run.
type Result struct {
	DayZeroClean bool
	Days         []Day
	FinalClean   bool // X == Z == fresh export at the end
}

func personOf(k crd.Key) string {
	if k.Kind == crd.KindPerson {
		return k.ID
	}
	return strings.SplitN(k.ID, "|", 2)[0]
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Run executes the scenario and narrates it to w.
func Run(cfg Config, w io.Writer) Result {
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }
	var res Result

	gen := source.NewGen(cfg.Seed)
	store := graph.NewStore()
	ledger := changelog.New(cfg.Seed + 1)
	ld := loader.New(store, ledger)

	// --- Day 0: initial ingest, export, baseline, day-zero recon -----------
	const t0 = int64(1000)
	x0 := gen.Initial(cfg.Members, t0)
	ld.Bulk(x0) // sync is OFF; nothing goes to the ledger
	export := store.Export()

	z := recon.Bootstrap(cfg.Buckets, export, t0) // shipped to Z: tree + leaf index + alias table
	xs := recon.Build(cfg.Buckets, source.Project(x0), recon.Version{TS: t0})

	p("== Day 0 (T=%d): initial ingest of %d members", t0, len(x0.Members))
	p("   Y export -> baseline tree shipped to Z: %d leaves in %d buckets", len(z.State.Index), cfg.Buckets)
	reports, compared := recon.Cumulative(xs, z.State)
	res.DayZeroClean = len(reports) == 0
	p("   day-zero recon  Merkle(P_X(X@T0)) vs Merkle(P_Y(export@T0)): %s (%d node comparisons)",
		map[bool]string{true: "MATCH", false: "MISMATCH"}[res.DayZeroClean], compared)
	p("   sync ON, ledger starts at SourceTS > %d", t0)

	prev := x0
	pendingRepairs := map[string]string{} // person ID -> repair ID
	groundTruthRepairs := map[string]bool{}
	repairSeq := 0

	for day := 1; day <= 6; day++ {
		T := t0 + int64(day)
		next := gen.Evolve(prev, T, cfg.ChangesPerDay)
		d := Day{N: day, Injected: map[string]loader.Fault{}, NewDrift: map[string]bool{},
			RepairsClosed: map[string]bool{}, RepairsOpen: map[string]bool{}, GroundTruth: map[string]bool{}}

		// Force a known change on a member and arm a fault for its writes.
		arm := func(f loader.Fault, change func(m *source.Member)) string {
			for {
				id := gen.Pick(next)
				if _, taken := d.Injected[id]; taken {
					continue
				}
				m := next.Members[id]
				change(&m)
				next.Members[id] = m
				ld.Inject(id, f)
				d.Injected[id] = f
				return id
			}
		}
		bumpTier := func(m *source.Member) {
			m.Tier = map[string]string{"bronze": "silver", "silver": "gold", "gold": "bronze"}[m.Tier]
		}
		switch day {
		case 1:
			for i := 0; i < 3; i++ {
				arm(loader.Duplicate, bumpTier)
			}
		case 2:
			arm(loader.DropLedger, bumpTier)
			arm(loader.TransformBug, func(m *source.Member) {
				if m.Status == "ACTIVE" {
					m.Status = "CLOSED"
				} else {
					m.Status = "ACTIVE"
				}
			})
		case 3:
			arm(loader.Late, func(m *source.Member) { m.Phones = append(m.Phones, gen.NewPhone()) })
		case 4:
			arm(loader.DropWrite, bumpTier)
			arm(loader.DropWrite, func(m *source.Member) { m.Phones = append(m.Phones, gen.NewPhone()) })
		}

		p("")
		p("== Day %d (T=%d)", day, T)
		for _, id := range sortedSet(func() map[string]bool {
			s := map[string]bool{}
			for id := range d.Injected {
				s[id] = true
			}
			return s
		}()) {
			p("   injected fault: %-13s on %s", d.Injected[id], id)
		}

		// Y applies X's changes; every write is recorded in the ledger.
		ld.Sync(prev, next)

		// Repair leg: Z -> Y -> X -> (X re-ships) -> Y -> ledger -> Z.
		for _, id := range sortedSet(func() map[string]bool {
			s := map[string]bool{}
			for id := range pendingRepairs {
				s[id] = true
			}
			return s
		}()) {
			rid := pendingRepairs[id]
			var m *source.Member
			if mm, ok := next.Members[id]; ok {
				m = &mm
			}
			ld.Repair(id, m, T, rid)
			if groundTruthRepairs[rid] {
				p("   repair %s: X re-shipped %s through Y (Z already agreed; only the next export can confirm it)", rid, id)
			} else {
				p("   repair %s: X re-shipped %s through Y", rid, id)
			}
		}
		pendingRepairs = map[string]string{}

		// Delivery: at-least-once, shuffled, possibly late.
		z.Receive(ledger.Deliver())
		if lw := ledger.LowWatermark(); lw <= T {
			d.Deferred = true
			p("   checkpoint T=%d DEFERRED: ledger low watermark %d <= T (late events outstanding)", T, lw)
			z.Receive(ledger.Deliver())
			p("   next delivery cycle: low watermark now past T, closing checkpoint")
		}
		staleBefore := z.Stale
		z.Advance(T)
		d.Stale = z.Stale - staleBefore

		// X side: patch from the snapshot diff.
		xst := recon.ApplySnapshot(xs, source.Project(next), T)

		dx, _ := xs.Checkpoint()
		dz, repairs := z.State.Checkpoint()
		p("   X patch: %d upserts, %d deletes (%d leaves unchanged, never rehashed)", xst.Upserts, xst.Deletes, xst.Unchanged)
		p("   Z patch: %d events applied, %d duplicates/stale ignored", z.Applied, d.Stale)
		z.Applied = 0

		// Compare today's increments, then explain each mismatched bucket.
		inc := recon.Increment(dx, dz)
		cum, _ := recon.Cumulative(xs, z.State)
		stillDiffers := map[int]bool{}
		for _, r := range cum {
			stillDiffers[r.Bucket] = true
		}
		for _, b := range inc {
			for _, rid := range repairs[b] {
				if stillDiffers[b] {
					d.RepairsOpen[rid] = true
				} else {
					d.RepairsClosed[rid] = true
				}
			}
			if len(repairs[b]) > 0 && !stillDiffers[b] {
				continue // today's mismatch is the repair landing; bucket now agrees
			}
			for _, ld := range recon.DiffBucket(xs, z.State, b) {
				pid := personOf(ld.Key)
				d.NewDrift[pid] = true
				p("   DRIFT bucket %3d  %-12s %s", b, ld.Kind, ld.Key)
				for _, a := range ld.Atoms {
					p("                      %s: X=%q Y=%q", a.Name, a.Left, a.Right)
				}
			}
		}
		for _, rid := range sortedSet(d.RepairsClosed) {
			p("   repair %s CLOSED (bucket now matches)", rid)
		}
		for _, rid := range sortedSet(d.RepairsOpen) {
			p("   repair %s STILL OPEN", rid)
		}
		d.CumulativeClean = len(cum) == 0
		p("   daily increment drift: %d member(s); cumulative X vs Z: %s",
			len(d.NewDrift), map[bool]string{true: "MATCH", false: "MISMATCH"}[d.CumulativeClean])

		for _, pid := range sortedSet(d.NewDrift) {
			repairSeq++
			pendingRepairs[pid] = fmt.Sprintf("R%03d", repairSeq)
		}

		// Periodic ground truth: re-export Neptune and compare against the
		// ledger-derived Z tree. This is the only check that sees writes the
		// ledger claims but Neptune never received.
		if day == cfg.GroundTruthDay {
			truth := recon.Build(cfg.Buckets, graph.Project(store.Export()), recon.Version{TS: T})
			gt, _ := recon.Cumulative(truth, z.State)
			p("   GROUND TRUTH: fresh neptune-export vs ledger-derived Z tree: %d bucket(s) differ", len(gt))
			for _, r := range gt {
				for _, l := range r.Leaves {
					pid := personOf(l.Key)
					d.GroundTruth[pid] = true
					// Cumulative(truth, z): the left side is Neptune, the right is Z.
					kind := map[string]string{
						"missing-in-Y": "Neptune has, ledger lacks",
						"extra-in-Y":   "ledger claims, Neptune lacks",
						"differs":      "ledger != Neptune",
					}[l.Kind]
					p("   GT   bucket %3d  %-28s %s", r.Bucket, kind, l.Key)
					for _, a := range l.Atoms {
						p("                      %s: Neptune=%q ledger=%q", a.Name, a.Left, a.Right)
					}
				}
			}
			for _, pid := range sortedSet(d.GroundTruth) {
				repairSeq++
				rid := fmt.Sprintf("R%03d", repairSeq)
				pendingRepairs[pid] = rid
				groundTruthRepairs[rid] = true
			}
		}

		res.Days = append(res.Days, d)
		prev = next
	}

	// Final: all three agree.
	truth := recon.Build(cfg.Buckets, graph.Project(store.Export()), recon.Version{})
	a, _ := recon.Cumulative(xs, z.State)
	b, _ := recon.Cumulative(xs, truth)
	res.FinalClean = len(a) == 0 && len(b) == 0 && len(z.Unresolved) == 0
	p("")
	p("== Final: X tree vs Z tree: %d diffs; X tree vs fresh export: %d diffs; unresolved edges: %d",
		len(a), len(b), len(z.Unresolved))
	return res
}
