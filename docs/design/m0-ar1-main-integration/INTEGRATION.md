# AR-1 main integration — affected qualification

Current integration candidate: `b875a35deeb7314fdfae127032d2d5a644a63759`. Parents: AR-1 `61d76463baf34090c16fe94669aa4dd0fa4655ad` and frozen main `d2dab994dd87445b5f5d44a06293bd5e09a1eee9`.

The [original fixed-61d76463 review package](https://github.com/VioletQwQ-0/matrixone/blob/d71475074e65764c6f4fc5194928e212cc7d2507/docs/design/m0-ar1-review-package/REVIEW_PACKAGE.md) remains immutable historical evidence. Reviewers must now inspect this merge delta as well; no previous review approves it.

## Resolution

- Preserve both statement-view lifecycle and upstream hash-existence helpers; keep both ColDef.NotNull and integer-result presentation deep copies.
- Use main's failure-atomic workspace admission, staged compaction records, bounded object-capacity ledger and asynchronous orphan handoff. Preserve AR-1's relation-qualified deletion/source identities and pinned old-generation schema.
- Keep explicit Engine layout checks with main's ReaderFilterRequest, error-returning copy-on-write Shrink and readonly physical-load APIs. No ObjectIO evolution permission is restored.
- Fix indirect caller/fixture integration: removed clusterByIdx, table-qualified summary maps, actual flush return values and explicit authority for controlled positive unit fixtures. Missing owner metadata rejects rather than panics; pure negative ADD gates remain before publication.
- Preserve panic output identities. Cleanup tests use main's existing bounded drain, then assert physical deletion and queue retirement; no synchronous cleanup under transaction locks is restored.

## Actual local validation

| Check | Result and reuse |
|---|---|
| Default unit | 42 roots passed; unchanged cases reused |
| M0 unit | 107 roots passed, including five precise guard/fixture controls |
| Ordinary SQL | Three roots: ADD/COPY, shared-object deletion and historical DUMP roundtrip passed |
| M0 SQL | 23 unique roots: 20 unchanged passing cases plus three revalidated error/cancel/panic cleanup cases |
| Race | 51 unit invocations and seven real SQL roots passed, including two-CN dispatch, clone ownership, cancel/panic, rollback and ADD/DML interleave |
| Static | Both modes incremental lint/molint; affected compile/vet, formatting, error and PR-relative diff checks passed |

The R1 selected-unit failures and R2 two cleanup failures are attached as historical attempts, not counted as final passes. R1 uncovered old shape-only fixtures; R2 captured the asynchronous-cleanup/diagnostic mismatches. The final receipt explicitly maps unchanged-case reuse and exact retests. Runtime source was frozen; following product documentation changes were semantic-neutral.

Go is 1.27.1 darwin/arm64. Native was rebuilt by top-level make from exact new main native inputs; it is not the old candidate's native. Library SHA and build-log hash are in the attached source binding. A reusable cross-worktree stamp is intentionally not issued for dirty merge inputs; none was fabricated. These are local author-reported results, not independent execution.

No new performance result is claimed. New-head CI, independent architecture/implementation acceptance, Linux, external target BVT and QA remain separate and pending. Same-version isolated experimental storage and the existing supported schema remain the boundary. Main-owned golden-file whitespace is preserved byte-for-byte; the PR delta against frozen main is whitespace-clean.

See [current source status](https://github.com/VioletQwQ-0/matrixone/blob/b875a35deeb7314fdfae127032d2d5a644a63759/M0_STATUS.md) and [current R3 addendum](https://github.com/VioletQwQ-0/matrixone/blob/b875a35deeb7314fdfae127032d2d5a644a63759/docs/design/20261027-m0-read-boundary-r3.md). Raw service logs and connection/host details are not published.
