# merkle-x

A complete, runnable example of **progressive Merkle reconciliation** between a
source dataset (X) and a graph representation of it (Y), checked by an
independent recon team (Z). Zero dependencies beyond the Go 1.24 standard library.
The data model is entirely invented.

```
go test ./...
go run ./cmd/sim
```

## The setup

```
Team X  daily snapshot X@T ───────────────────────────────┐
   │                                                       │
   │ changes                                               ▼
   ▼                                                    Team Z
Team Y  loader ──► graph (Neptune stand-in)          Tree_X(T): patched from snapshot diffs
   │                  │                                Tree_Z(T): baseline export + changelog
   └──► changelog ────┼──────────────────────────────►  compare daily increments
                      │                                 localize to member and property
                      └── neptune-export (day 0 and periodic ground truth)
```

1. Initial ingest of X@T0 into the graph, with sync **off**.
2. Export the graph, project it, build the baseline tree, and ship it to Z with
   its leaf index and alias table.
3. Day-zero recon: `Merkle(P_X(X@T0))` against `Merkle(P_Y(export@T0))`.
4. Turn sync on. Every loader write is recorded in the changelog with
   `SourceTS` in X's version domain.
5. Each day, Z patches both trees, compares the day's per-bucket increments,
   and localizes any mismatch down to member and property.
6. Discrepancies route Z → Y → X; X re-ships through Y; the repair comes back
   through the changelog tagged with its repair ID, and Z confirms closure.
7. Periodically, Z compares a fresh export against its ledger-derived tree.
   This is the only check that sees writes the ledger claims but the graph
   never received.

## Invented data model

| Canonical (CRD) | X representation | Y representation |
|---|---|---|
| Person `name`, `status`, `tier`, `country` (mutable) | `Member{DisplayName, Status: ACTIVE, Tier: gold, ...}` | `Member` vertex: `full_name`, `state_cd: A`, `loyalty_tier: G`, `iso_country` |
| phone, canonical `+15550102030` | free-form string in `Phones[]` | permanent `PhoneNumber` vertex, `digits: 15550102030`, loader ID `pn-000123` |
| email, canonical lowercase | free-form string in `Emails[]` | permanent `EmailAddress` vertex, stored as received, loader ID `ea-000456` |
| edge `has_phone` / `has_email` | implied by nesting | `HAS_PHONE` / `HAS_EMAIL` edges |

Phone and email vertices are **permanent**: created once, shared by every
member referencing that value, never modified or deleted. Only their edges to
a member change.

## Leaf design

**One mutable core vertex = one leaf, hashed as a sum of property atoms.**

```
atom(k, name, value) = LtHash(kind, id, name, value)
leaf(Person p)       = Σ atom(p, prop, value)  over name, status, tier, country
```

A property change is `bucket += atom(new) - atom(old)`. A mismatch localizes
to the property for free.

**Permanent vertices get no leaf of their own.** Their canonical ID is
content-addressed from their normalized value, so the ID determines the value.
The only thing that can change about them is the relationship, which is the
edge leaf:

```
leaf(edge) = Σ atom over {from: personID, label, to: H(value), value}
```

This avoids reference counting and the orphaned-vertex question entirely. A
corrupted phone value in the graph re-derives to a different ID, so it still
surfaces as an edge mismatch.

**Every leaf is bucketed by its Person ID.** A member's leaves share a bucket,
so the tree localizes to a member, yet no update ever needs the whole member
materialized.

**No derived fields in any leaf.** Anything computed from children (a
"primary phone" on the person, a contact count) would force a person rehash on
every child change.

## Why LtHash instead of XOR

Bucket accumulators are LtHash16: 1024 × 16-bit lanes, added mod 2^16.
Addition is commutative, associative, and invertible, so inserts add, deletes
subtract, and order does not matter. Unlike XOR, a double-applied event does
**not** cancel itself out, so duplicate delivery becomes visible instead of
silently corrupting the tree.

## Correctness conditions the code enforces

| Condition | Where |
|---|---|
| Fixed tree shape: buckets by `hash(root) mod n`, independent of data | `crd.Bucket`, `tree` |
| Dirty-path recompute only at checkpoint, O(log n) per dirty bucket | `tree.Root` |
| Per-leaf version `(SourceTS, Seq)`: duplicates and reordering ignored | `recon.State.Put/Delete` |
| Tombstones: a late older add cannot resurrect a deleted leaf | `recon.Entry.Deleted` |
| Checkpoint T closes only when the ledger low watermark is past T | `changelog.LowWatermark`, `recon.Consumer.Advance` |
| Events applied in `(SourceTS, Seq)` order; later events held for the next checkpoint | `recon.Consumer.Advance` |
| P_Y is written independently of the loader's mapping | `graph.Project*` vs `loader.memberVertex` |
| Removes before adds when two Y edges project to one canonical edge | `loader.Sync`, `loader.Repair` |

## Daily increment comparison

Both sides record per-bucket deltas since the last checkpoint. Z compares
**today's change** on each side:

```
new_drift_T = δZ_T - δX_T     (per bucket)
```

A nonzero bucket means something went wrong today, not at some point since T0,
and neither side ever rehashes the full dataset after day zero. A mismatch in a
bucket touched by a repair is the repair landing; if the cumulative trees agree
in that bucket afterward, the repair is closed.

## What the simulator demonstrates

| Day | Injected fault | Outcome |
|---|---|---|
| 0 | none | Day-zero recon matches |
| 1 | at-least-once duplicates | Ignored by version gating; zero drift |
| 2 | ledger drops a write; loader status-mapping bug | Both caught daily, localized to member and property |
| 3 | events delivered late; yesterday's repairs re-shipped | Checkpoint defers until the watermark passes; repairs confirmed closed |
| 4 | ledger records writes Neptune never received | **Invisible to the daily check.** This is the optimistic-ledger blind spot |
| 5 | none; periodic ground-truth export | Finds day 4's lost writes, including a lost permanent phone vertex |
| 6 | ground-truth repairs | Final check: X tree, Z tree, and a fresh export all agree |

`sim_test.go` asserts each of these across several seeds.

## Package layout

| Package | Role |
|---|---|
| `lthash` | LtHash16 homomorphic accumulator |
| `crd` | Canonical leaves, normalization, content-addressed IDs (owned by Z) |
| `tree` | Fixed-shape Merkle tree, lazy dirty paths, BFS diff |
| `source` | X's nested representation, synthetic data, P_X |
| `graph` | Y's property-graph representation, export, P_Y |
| `loader` | Y's X→Y mapping, writes, changelog emission, fault injection, repair |
| `changelog` | Ledger with shuffled, duplicated, late delivery and a low watermark |
| `recon` | Progressive state, leaf index, snapshot patching, changelog consumer, comparison |
| `sim`, `cmd/sim` | End-to-end scenario |

## Mapping to production

- **Baseline:** neptune-export (clone-cluster mode) to S3, then a Glue or EMR job
  that projects and sums leaves per bucket. Partial accumulators combine in any
  order, so it parallelizes trivially.
- **Leaf index:** `(kind, id) → (version, 32-byte digest, bucket)` in DynamoDB or
  bucket-partitioned Parquet. This example keeps full leaves in memory only to
  print property-level diffs.
- **Alias table:** Y vertex ID → canonical ID, shipped with the baseline and
  maintained from vertex upsert events.
- **Daily run:** a Step Functions flow per checkpoint: wait for the watermark,
  apply buffered events, patch the X tree from the snapshot diff, compare
  increments, write discrepancies.
- **Lane count and bucket count** are tunable. 2 KB per bucket accumulator
  × 2^16 buckets is 128 MB of state.

## Simplifications

- Single process, in memory; no real AWS calls.
- Repairs always succeed on the first attempt. A systematic loader bug would
  re-fail every repair, so production needs a retry bound and escalation.
- The X-side daily diff is computed from full snapshots; if X publishes deltas,
  use those instead.
