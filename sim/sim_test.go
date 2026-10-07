package sim

import (
	"io"
	"testing"

	"github.com/miketheprogrammer/merkle-x/loader"
)

func injectedWith(d Day, faults ...loader.Fault) map[string]bool {
	out := map[string]bool{}
	for id, f := range d.Injected {
		for _, want := range faults {
			if f == want {
				out[id] = true
			}
		}
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func TestScenario(t *testing.T) {
	for _, seed := range []int64{1, 42, 2026} {
		cfg := DefaultConfig
		cfg.Seed = seed
		r := Run(cfg, io.Discard)

		if !r.DayZeroClean {
			t.Fatalf("seed %d: day-zero recon must match", seed)
		}
		d1, d2, d3, d4, d5 := r.Days[0], r.Days[1], r.Days[2], r.Days[3], r.Days[4]

		if len(d1.NewDrift) != 0 || d1.Stale == 0 {
			t.Errorf("seed %d day 1: duplicates must be ignored without drift (drift=%v stale=%d)", seed, d1.NewDrift, d1.Stale)
		}
		if want := injectedWith(d2, loader.DropLedger, loader.TransformBug); !sameSet(d2.NewDrift, want) {
			t.Errorf("seed %d day 2: drift %v, want exactly %v", seed, d2.NewDrift, want)
		}
		if !d3.Deferred || len(d3.NewDrift) != 0 || len(d3.RepairsClosed) != 2 || len(d3.RepairsOpen) != 0 || !d3.CumulativeClean {
			t.Errorf("seed %d day 3: want deferred checkpoint, 2 closed repairs, no drift; got %+v", seed, d3)
		}
		if len(d4.NewDrift) != 0 || !d4.CumulativeClean {
			t.Errorf("seed %d day 4: drop-write must be invisible to the daily check (that's the blind spot)", seed)
		}
		if want := injectedWith(d4, loader.DropWrite); !sameSet(d5.GroundTruth, want) {
			t.Errorf("seed %d day 5: ground truth found %v, want exactly %v", seed, d5.GroundTruth, want)
		}
		if !r.FinalClean {
			t.Errorf("seed %d: X, Z, and a fresh export must all agree at the end", seed)
		}
	}
}
