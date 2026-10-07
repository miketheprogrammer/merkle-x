// Command sim runs the end-to-end progressive Merkle reconciliation scenario.
package main

import (
	"flag"
	"os"

	"github.com/miketheprogrammer/merkle-x/sim"
)

func main() {
	cfg := sim.DefaultConfig
	flag.IntVar(&cfg.Members, "members", cfg.Members, "members in the initial snapshot")
	flag.IntVar(&cfg.Buckets, "buckets", cfg.Buckets, "tree buckets (power of two)")
	flag.IntVar(&cfg.ChangesPerDay, "changes", cfg.ChangesPerDay, "random X changes per day")
	flag.Int64Var(&cfg.Seed, "seed", cfg.Seed, "random seed")
	flag.Parse()

	res := sim.Run(cfg, os.Stdout)
	if !res.FinalClean {
		os.Exit(1)
	}
}
