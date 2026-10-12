# FT2 query-scoped diagnostic receipt v1

Base: 7447e4bd633730d4b90241273d2f7bf1cc0cbe69. Authorized by the user on
2026-10-12 solely for observation and one minimal C Q1 probe. No retrieval,
planner policy, SQL result, cache configuration or historical verdict changes.

Design-first review: PASS before implementation. Trigger: a query-scoped
observation crosses the TVF/search boundary and executes on a hot path. The
owner is one TVF invocation, not the cached Index. The existing Fulltext2Query
payload carries an optional diagnostic pointer through the synchronous search.
For streaming, ownership transfers to its existing producer until search ends;
no new worker/channel, retained history, shared per-query map or metric labels.
Two bounded log envelopes are emitted: the compiler/statement binding before
search and the terminal search receipt. The consumer independently obtains
last_query_id (the frontend statement UUID, distinct from Process.QueryId) and
the actual SQL hash from its owned client, joins the envelopes
on statement/node/invocation/round and hashes, and rejects duplicates/mismatch.

Enabled only by MO_FT2_QUERY_DIAGNOSTIC=1 in an isolated diagnostic CN AND the
literal SQL comment `/*ft2-diagnostic*/`. The comment does not change semantics;
its actual SQL hash is recorded. Setup/convergence queries lack the marker and
do not consume receipts. Defaults off; at most 64 invocation receipts per process, with a unique process-local
sequence, query/statement IDs, node/operator/parallel IDs and actual SQL hash.
The compiler also snapshots/hash-binds its actual query plan into the existing
Params transport (default-off and non-empty Params left untouched). The observer
proves no residual qualification only for one Project/Sort chain to the FT2
node with no FilterList or runtime probe, and emits unknown for other topology.
The actual runtime receipt binds that metadata to statement/node/invocation;
the argument/limit digest remains a separate subtree hash. Unknown is never
serialized as false. Exhausted receipt budget means missing evidence,
not execution failure. No query text, predicate values, keys or raw errors.

Record planned/effective TVF limit, membership fallback reason, SearchInto/Emit
route, search-side RuntimeConfig.Limit/resolved K, predicate digest and typed
column/op descriptors (bounded), actual predicate evaluation/rejection/read
error counts, pre/post top-K result counts and score-range rejection count.
Do not infer an exact predicate from INCLUDE projection or no filter text.

Early return/error/cancel is not a successful search. Invocation-local data is
sealed after synchronous search returns, or in the existing streaming producer
after its terminal channel close (so logging does not delay drain/reset);
reset never reuses this pointer. Panics do
not generate a success receipt. Logging serialization failure cannot alter SQL
behavior. Observer is off during performance measurements. Disabled mode does
not allocate receipts or hash SQL; enabled mode adds bounded counters and one
bounded JSON log, intentionally unsuitable for timing.

Alternatives: plan-only evidence cannot expose limit clearing or Emit;
general tracing framework/global index counters lose query ownership and add
unnecessary state. Use the existing structured logger, local opt-in and payload.
No wire/catalog/persisted-index format change. Removal/rollback is restart with
the flag absent or reverting this diagnostic-only commit.

Verification: enabled/off result equivalence using the existing tiny index;
exact IS NULL evaluation including rejected non-NULL candidates; effective
limit and top-K counts; Emit route and cancellation/error; nil/PASS membership
clearing; fresh receipt on reused operator; bounds/redaction, missing identity,
and fail-closed collector tests. Full local owning-package checks precede any
build/dispatch. Probe requires correct Top200 oracle, complete identity and all
four mechanism gates. Compiler metadata is observational only, copied by the
existing TableFunction Params copy/serialization paths; no new wire field or
planner decision. Probe joins it with the actual INCLUDE/top-K/search receipt,
never takes metadata alone as proof of search. No real data or performance.

Design update review: PASS for this metadata-only compiler reverse arc. B or
an unknown operator ancestor remains UNKNOWN rather than an optimistic false.

## Local verification (2026-10-12)

Owning fulltext2, table_function and compile package normal tests with vet=all:
PASS. Compiler metadata opt-in test under MO_FT2_QUERY_DIAGNOSTIC=1: PASS.
Owning fulltext2 and table_function package race tests: PASS. The exact
TestQueryDiagnosticConcurrentOwnership measured below timer resolution under
race, hence the 30-second adaptive budget selected count=100: PASS.
No service/TKE/performance result is implied by these local checks. Existing
membership/stream cancellation tests are included in owning-package checks.

Self-review: default-off output equivalence, invocation ownership, stream
close-before-log ordering, early return/cancel/error, no cached diagnostic
pointer, bounded emitted descriptors and identity fields, unknown residual
topology, existing Params copy/wire paths and independent client statement
binding reviewed. The SQL client UUID and execution Process.QueryId are
distinct identifiers and must not be compared directly. Ordinary BVT cannot
assert this private administrative log envelope; the authorized C Q1 probe
is the outstanding real SQL/receipt/oracle gate. Original evidence stays
INCONCLUSIVE, and diagnostics-enabled queries are not performance samples.
