# Issue29322: frozen-T matcher experiment, revision 2

Owner: issue #29322. Experimental source branch only; no production PR or algorithm selected.
Base T: c3c458159523242bf84e3249ef8c28f22b631d9b. Tool PR241 remains Draft.

## Evidence and goal

Same 10M logical data/4128 native requests: G omitted all4000 ordinary results; B returned40186 correct ordinary rows. Ordinary B made140812 base matching calls,100794 with no hits. Trusted per-block UINT64 ranges reduce summed candidate sizes1527010 to182808 offline. This is neither CPU gain nor the fraction of the historical TPS loss. We must preserve full results while evaluating total reader cost.

Route A reuses the existing generic/linear/binary/hybrid experiment and197 unchanged cases. Hash remains legacy-only until reader/concurrent memory is proven. Route B evaluates a range subset with the original generic matching semantics, independently of route A. No arbitrary threshold search, combined unqualified optimization, SQL setting, format, cache, or system policy is added.

## Alternatives and choice

1. Keep full candidate vectors: simplest correct baseline, no additional metadata plumbing.
2. Fetch zone maps again in each matcher: correct with provenance but additional cache/IO lookup; rejected because diagnostic timing cannot prove that cost.
3. Reuse metadata already returned by LoadColumnsData: selected experimental seam. LoadColumns currently discards it. An optional scalar range callback on the internal BlockReadFilter receives only copied UINT64 bounds after normal column loading. Existing vectors-only public APIs remain wrappers with nil callback.
4. Infer fake-PK physical ordering or retain only one candidate: rejected; neither invariant is true.

The set-theoretic proof is ordinary interval exclusion: if every physical value is inside trusted[min,max], a query value outside that interval cannot match. Values inside the interval still require exact matching; overlapping, duplicate and non-reducing intervals remain valid. No protocol or storage standard changes.

## Ownership and data flow

Reader owns the original borrowed candidate vector through its existing cleanup owner. The experimental constructor checks actual candidate order once and never sorts/modifies it. No cross-reader or cross-binding subset cache exists. A subset is a call-local slice, never retained across blocks. Original generic fallback remains available.

ReadDataByFilter's existing public signature remains compatible. A private implementation accepts an optional callback. BlockDataRead passes it only for fake-PK unsorted filtering. A private readBlockData variant optionally retains ObjectDataMeta returned by the same LoadColumnsData call; ordinary nil paths still call LoadColumns. No extra metadata load occurs.

The metadata belongs to the exact location loaded for this read. Verify object short-name, block ID range, filter sequence number and UINT64 column/type, initialized zone map, fixed min/max width and min<=max. Only two scalar uint64 bounds cross the callback; no metadata slice survives its owner or escapes to a search closure. Appendable blocks, absent/uninitialized/wrong-type metadata and unordered candidates use the full matcher. Storage/read errors follow the existing error path and must not be converted to a fallback.

The existing release and delete-mask defers run on every return. Exact matching produces the original increasing offsets, then the unchanged delete/snapshot/tombstone handling runs. Callback has no waits, goroutines, FDs, persistent state or additional cleanup. Cancel/read failure occurs before callback, retains original error and releases acquired resources. Reset/Close drop the filter through the existing owner; no independent retained state exists.

## Admission and compatibility

Public ConstructBlockPKFilter and NewReader keep nil experiment factories. No SQL execution can activate either prototype. Only package-local tests/benchmarks set the range callback on a fake-PK UINT64 IN filter. Wrong types, disjunction/membership or other dispatch domains retain the baseline. Experiment-only seams will be removed before a qualified production change. There is no wire/disk/catalog/global/tenant/upgrade state; defaults stay baseline.

## Cost model and bounds

Route A costs include construction/sorted verification, matching, cross-block reuse and Close, as before. Route B pays one actual-order check O(M), then two binary bounds O(logM) per eligible block and exact matching on the retained candidates. With no reduction it pays this overhead and must still meet<=3% regression. No additional candidate vector copy or per-reader map is added. Call-local output allocation matches baseline. Original197 scenarios are untouched; metadata absent/overlapping/no-reduction/unsorted/lifecycle controls supplement them. A subset-factory closure per block, if retained initially, is counted rather than hidden.

Real block tests use a few persisted UINT64 rows and the existing local file-service writer fixture, no MO process or10M preparation. They prove block identity, exact rows, deletes, unsupported/appendable fallback, cancellation/read errors and cleanup. Performance/qualification use native Go1.27.1 plus the original manifest, not UT wall time.

## Verification and stop rules

- Reuse original algorithm boundary oracles, hardware counter tests and197 case definitions unchanged.
- Test subset empty/one/duplicates/UINT64 extremes/overlap/no reduction/order/fallback without modifying borrowed input; verify independent reader and Reset/Close ownership.
- Exercise actual BlockDataRead and metadata returned by its existing loader; direct helpers do not replace the persisted consumer. Unit-test scalar-range validation and original error semantics.
- Native hosted build, named focused tests and affected owning packages must pass on exact T experimental SHA; build/race/static evidence remains separate from qualified performance. Local native build is not admitted while disk is insufficient.
- First controlled129 run checks original stability before screening; instability stops INCONCLUSIVE without core hopping/relaxation. Only stable measurement permits full197/5screen/10confirm, main improvement>=max(10%,2J) and every primary/control loss<=3%. Hash memory is an independent rejection gate.
- No production PR before qualification. Later production public NoPK/prepared/lifecycle regression and BVT/CI/TPS/QA remain mandatory. This test-only inactive experiment has BVT:N/A because no SQL activation exists.

## Design decision

Complexity triggers: internal cross-package contract and material hot path. Risk:R3 for optional block metadata/reader boundary, R1 for unchanged test-only algorithms/hardware fixtures. The plan authorizes the experiment; code/review quality is assessed before delivery. Existing error/cleanup owners remain unchanged; new callback is stateless and bounded. Design revision1 approved for experimental implementation, not production or performance acceptance. Any required mutable state, extra IO or public activation invalidates this decision and requires re-review.

## Revision 2: personal Linux validation and measured range construction

The user authorized correctness and component measurements on personal Ubuntu
100.89.164.105. All 129 use remains paused. No database, full data preparation,
system policy changes, production activation or TPS acceptance is in scope.

The range factory must be installed by the internal experimental constructor,
using the actual owned decoded candidate vector and original generic fallback.
Public constructors pass no factory. Admission is limited to one UINT64 IN fake-PK
predicate without membership/disjunction. Range and specialized factories cannot
be combined. Existing cleanup owners and failed-constructor cleanup stay intact.

The original 197 keys/inputs remain unchanged. The range variant prepares real
persisted metadata from the input rows outside the benchmark timer; empty row
fixtures have unavailable metadata. Each timed search obtains and validates its
block range through the same scalar helper. Reader timing includes candidate
decode, original filter construction, actual-order check, ranges and Close.
The seven persisted block keys use normal BlockDataRead metadata and loading.
Additional fixed metadata controls supplement, never replace, the 197 keys.

Native run38720663071 passed named range consumers and readutil owning tests;
objectio fixture compilation failed and blockio owning did not run. The fixture
fix and revised full T-to-subject fingerprint require complete hosted validation.
Personal execution uses the native runtime closure, one pinned logical CPU,
GOMAXPROCS=1/GOMEMLIMIT=512MiB, user systemd 4GiB/zero swap/256 tasks and an
independent bounded watchdog. No sudo/linger/governor/perf permission changes.
Exact-run release is required before the next stage. Original stability/197/5+10
rules remain; unavailable original metrology permits DIAGNOSTIC_ONLY reporting.
All ranges/CPU/TPS claims remain separate. This revision is approved for inactive
experimental implementation and personal-host admission, not product delivery.
