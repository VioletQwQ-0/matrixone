# Bounded FULLTEXT2 search diagnostics

This diagnostic build supports the ANLI follow-up to
[TopK/INCLUDE #30193](https://github.com/matrixorigin/matrixone/pull/30193),
using merge commit `77ccd7c43890bb3f3d1759bb2dc6cfa30519248e` as its code base.
It measures the remaining search work before selecting a performance change.
The existing CPU profiles belong to PR head `b547bc0...`, not this merge commit.

## Scope and invariants

The experiment selects four MATCH payloads (parameter IDs 13, 89, 66, 23).
Diagnostics must preserve PKs, term frequencies, score bits, eligibility,
ranking, global DF, index generations and cancellation behavior. They do not
change the index format, cache policy, optimizer or background tasks.

Each invocation owns its counters. A diagnostic search borrows the immutable
index under the existing cache read lock and shallow-copies only its Index and
Segment headers. Posting decode helpers receive the optional counter explicitly;
no counter is stored on a build-side or loaded posting directory.
The view does not own shared buffers and must never be freed as an index.
Ordinary searches use the original index with nil counters.

Threading a new parameter through every phrase/prefix/scoring helper would
change many unrelated call contracts. The borrowed view keeps instrumentation
at the operations it measures without putting mutable state on shared indexes.
It adds a pointer to Segment and the query-local cursors; allocation and latency controls
must quantify the disabled-path cost. This is an evidence build, not a proposed
production performance optimization.

## Enablement, bounds and receipt

`MO_FT2_SEARCH_DIAGNOSTICS` is absent by default. It accepts up to four comma-
separated SHA256 hashes of MATCH payload bytes, or `@path` naming a run-owned
file containing those hashes. The file is limited to 260 bytes. An absent,
malformed, duplicate or oversized configuration enables nothing. A valid file
may be published after data loading and parameter-pack verification. Its first
valid contents are fixed for the process lifetime; changing the file does not
reload the configuration. There is no watcher or background worker.

A process emits at most three receipts per selected hash (twelve total), with
atomic admission before execution. The receipt carries the process query ID,
a hash of the immutable scan specification, payload/predicate hashes, a sample
number, planned/reader limits and the limit actually passed to the kernel.
Only hashes and scalar counts are logged; SQL, tokens, PKs and error text are
excluded. The ANLI collector must match the payload hash to its parameter ID
and require exactly one matching FT2 scan in the captured plan. Ambiguous node
binding, absent query identity or a missing receipt remains INCONCLUSIVE.

Counters distinguish FST lookup/prefix expansion, directory header/full-entry
decode, posting and position blocks, materialization, score contributions,
candidates and WAND bounds/moves. `*_slice_bytes` records requested backing
storage for the counted buffers, excluding allocator rounding, diagnostic
headers and pooled capacity. It is not total heap allocation. Allocation
profiles and local `allocs/op` provide that separate evidence.

Success is reported as COMPLETE, not mechanism PASS. Errors and cancellation
have separate terminal statuses. A cleared LIMIT/stream route emits
INCONCLUSIVE without kernel counters; asynchronous stream completion is not
claimed. All diagnostic executions are excluded from performance samples.

## Validation and use

Focused tests compare PK sets, score bits and posting TF/position values with
diagnostics disabled; they cover loaded/build-side representations, prefix,
phrase, WAND, NULL eligibility, errors, output reuse and concurrent searches.
Independent query counters and the admission bound require race validation.
The planner test covers the actual text-protocol Q1/Q3 SQL shapes, including
B's residual eligibility control and C's covered Top200.

The live follow-up must independently verify MO/image identity, original source
bytes, row count, parameter-pack hash, query/scan binding and cleanup. The old
loaded-row hash `ac539166...` differs from the reproducible newer
`cf51ddd9...`; archived aggregate hashes cannot identify a differing column.
Unless the old row identity is reproduced, future pairs form a new experiment
and cannot use the old measurements as a numeric baseline.

Only after the four-query evidence identifies a reducible shared hotspot is a
single optimization selected. Search-layer correctness and three paired runs
must pass before the separate Q1/Q3 concurrent business experiment.
