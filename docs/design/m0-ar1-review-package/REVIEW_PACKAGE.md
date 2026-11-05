# PR #30816 — AR-1 fixed-candidate review package

Prepared 2026-11-05. Published review handoff; no independent approval is asserted.

## Review request

Please review the complete AR-1 candidate for the restricted metadata-only nullable ADD feature. The change replaces shape-derived missing-column authority with a persisted relation evolution contract, moves schema-evolution interpretation into Engine, preserves statement-visible state on the supported remote scan path, and restores the ordinary default reader contract.

The requested outcome is a concrete architecture and implementation decision for this candidate. Author-reported local checks are complete; independent review and maintainer acceptance remain pending. This package preparation is not a new full-code review and does not certify the absence of defects. No reviewer response, PR review state or remote head was refreshed for this document.

## 1. Exact review identity

- Local branch: `codex/m0-ar1-local`; worktree clean at preparation.
- Series base: `667fde44fb1aaae6247b64a151bd41ae2c4e3042`. This is the existing local PR-development base, **not a newly verified current main or remote PR head**.
- Review head: `61d76463baf34090c16fe94669aa4dd0fa4655ad`.
- Final tree: `189f65182fba2b0a27a71f6a13faf3c5a1a10c4f`.
- Frozen patch SHA-256: `b5808a9e96b7647992842b9454f6c53387b14d9142f7f86617f119f3f05a3264`.
- Frozen archive SHA-256: `facd69ebcd57a0b35956153ec6a48e5050ac5e7e57e9782ab414d4d50e82a9d6`.
- Go: `go1.27.1 darwin/arm64`; native dylib SHA-256: `ca867dd5aee378eddcf7c89f5eff018414d26f3bc8ec05f2b74bf696303dc14c`.
- 171 sealed files match the committed final tree; the eight retired paths are absent. All eight top-level receipt/document hashes in the final manifest were read back and matched during package preparation. The historical working journal itself is not republished; its original hash remains in the manifest.

[ar1-split-commit-binding.json](attachments/ar1-split-commit-binding.json) records the split from the original single local commit. The final tree is identical; existing final-tree validation is reused. **Intermediate commits were not independently compiled or qualified.** Review the complete series and inherited interactions, not just the last cleanup commit.

| Order | Commit | Purpose |
|---|---|---|
| 1 | `d3576557c5c995ad4c22f8996cb1001f1a165445` | feat(catalog): add persisted M0 evolution contract |
| 2 | `3d2a96a4eda52fecf694b09a2c3a310a5007b2d5` | feat(engine): add strict M0 read layout mapping |
| 3 | `f26509de9ef6f7bbbb41a1c39526b271400894fd` | feat(disttae): bind M0 authority and remote statement views |
| 4 | `61d76463baf34090c16fe94669aa4dd0fa4655ad` | refactor(objectio): retire M0 policy and restore default reads |

Read-only entry points, from the isolated worktree:

```sh
git log --reverse --oneline 667fde44fb1aaae6247b64a151bd41ae2c4e3042..61d76463baf34090c16fe94669aa4dd0fa4655ad
git diff --stat 667fde44fb1aaae6247b64a151bd41ae2c4e3042 61d76463baf34090c16fe94669aa4dd0fa4655ad
git diff 667fde44fb1aaae6247b64a151bd41ae2c4e3042 61d76463baf34090c16fe94669aa4dd0fa4655ad
```

The four-commit series includes inherited local work. It must not be described as four independently shippable releases or as a new diff against current main. If the publication target has drifted, inspect that integration delta before publishing or importing qualification.

## 2. Architecture and supported boundary

**M1 — authority.** `SchemaExtra.M0Evolution` is an explicit versioned, O(columns) persisted contract. It binds root, account/database/relation, stable column semantics and introduction generations. Authoritative CN/TN schema projections validate the contract; MVCC object selection supplies snapshot-visible membership. Neither object shape, OID, width, writer ID nor schema version alone grants authority. No permanent per-object provenance ledger is introduced.

Catalog schema version and object layout generation have different meanings. The existing object header version field denotes layout generation for contracted objects. This is a semantic change even though the physical object encoding is unchanged. The new SchemaExtra/protobuf metadata semantics must be reviewed explicitly.

Controlled CREATE establishes a root; an uncontracted normal source ADD uses COPY to establish a rewritten root. Contradictory authority is an error, not a clear-and-COPY fallback. M0 ADD atomically advances schema and contract. Clone inherits source-snapshot interpretation and rebinds the target. Writers, flush and merge use validated generations; supported WAL/checkpoint recovery reconstructs authority. Contract targets reject unsupported raw physical table LOAD before installation. Generated-column tables remain ordinary COPY controls, not M0 roots.

**M2 — execution.** Engine owners bind relation/schema/snapshot and pass explicit bounded `ObjectReadLayout` state. Missing nullable suffixes are materialized only after authority and declared layout validation. Special optimizations retain their physical prerequisites; otherwise existing exact filtering, sorting or aggregation executes. This does not introduce a simple/complex SQL classifier.

Supported remote execution fixes statement-visible state before dispatch, preserving committed reconstruction and coordinator contributions. The tested two-CN path includes uncommitted persistent data/deletes, deletedBlocks and coordinator memory. It is not a generalized combined-member protocol or permission for partition M0 ADD. No new wire payload is claimed.

**M3 — compatibility.** Ordinary default missing-column behavior is restored to the pre-PR branch identified as `2465dae7`, with existing physical checks retained. Strict M0 failure never grants ordinary NULL fallback. The exact Engine-owned system-catalog compatibility rule is separate. ObjectIO retains physical location, read/decode and generic physical checks; M0 context permissions and shape-derived authority are removed.

Experimental storage is same-version and isolated by storage namespace/directory, permissions and deployment. Old/default binaries are **not** guaranteed to reject M0 data. Mixed-version operation, downgrade, production enablement and general physical backup/restore qualification are not supplied. Rewriting a current table does not migrate retained snapshot/clone references.

## 3. Review map and decisive questions

| Closure | Main entry points | Required review decision |
|---|---|---|
| Persisted authority and semantic projection | [proto/api.proto](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/proto/api.proto); [pkg/internal/m0read/evolution.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/internal/m0read/evolution.go); [pkg/internal/m0read/semantics.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/internal/m0read/semantics.go) | Do normalization, version handling, owner binding and schema/contract atomicity define an unambiguous restricted evolution contract? |
| CN publication and read binding | [pkg/vm/engine/disttae/m0_evolution_mo.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/vm/engine/disttae/m0_evolution_mo.go); [pkg/vm/engine/disttae/txn_table.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/vm/engine/disttae/txn_table.go); [pkg/sql/compile/ddl.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/sql/compile/ddl.go) | Can stale plan/owner authority, no-contract state or COPY/clone publication accidentally authorize NULL? |
| Producers and TN recovery | [pkg/vm/engine/tae/catalog/m0_evolution_mo.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/vm/engine/tae/catalog/m0_evolution_mo.go); [pkg/vm/engine/tae/catalog/schema.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/vm/engine/tae/catalog/schema.go); [pkg/vm/engine/tae/tables/jobs/flushTableTail.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/vm/engine/tae/tables/jobs/flushTableTail.go); [pkg/vm/engine/tae/tables/jobs/mergeobjects.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/vm/engine/tae/tables/jobs/mergeobjects.go) | Are generations bound to actual written layouts, including pinned older clone rewrites, rollback and recovery? |
| Statement visibility and resources | [pkg/internal/m0read/statement.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/internal/m0read/statement.go); [pkg/sql/compile/scan_scheduler.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/sql/compile/scan_scheduler.go); [pkg/sql/compile/scope.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/sql/compile/scope.go); [pkg/vm/engine/disttae/local_disttae_datasource.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/vm/engine/disttae/local_disttae_datasource.go) | Are coordinator contributions complete and immutable for this statement; do retry/cancel/partial dispatch terminate references before releasing resources? |
| Engine interpretation and consumers | [pkg/internal/m0read/layout.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/internal/m0read/layout.go); [pkg/internal/m0read/read_layout.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/internal/m0read/read_layout.go); [pkg/vm/engine/tae/blockio/m0_mapping.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/vm/engine/tae/blockio/m0_mapping.go); [pkg/vm/engine/readutil/reader.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/vm/engine/readutil/reader.go) | Do pruning/statistics/COUNT/TopN and TN scans use the same authorized meaning, including empty selection, all-NULL projection and tombstones? |
| Default and mechanism retirement | [pkg/objectio/funcs.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/objectio/funcs.go); [pkg/objectio/schema_version.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/objectio/schema_version.go); [pkg/objectio/missing_column_test.go](https://github.com/VioletQwQ-0/matrixone/blob/61d76463baf34090c16fe94669aa4dd0fa4655ad/pkg/objectio/missing_column_test.go) | Is default restored without giving strict callers a fallback escape, and are all retired policy consumers replaced? |

The finite consumer and retirement inventories are in [A1_BINDING_AND_RECOVERY.md](EVIDENCE_SUMMARY.md). This map is a navigation aid, not a substitute for reading all changed hunks and their affected callers, including inherited catalog/backup/partition changes.

## 4. Response to architecture concerns

| Concern | Candidate response | Evidence / remaining boundary |
|---|---|---|
| ObjectIO decides whether schema evolution permits NULL | Engine validates persisted authority and creates explicit layouts; ObjectIO no longer carries context NULL permission | Mapping, real reader, metadata and TN merge controls; architecture acceptance still requested |
| Shape/version cannot prove provenance | Explicit relation contract plus snapshot-visible MVCC membership; no shape-based promotion of imported objects | Real CREATE/COPY/ADD/clone and inconsistent-authority rejection; arbitrary forged cross-relation RelData is not a trusted entry |
| Default historical reads globally tightened | Restore ordinary pre-PR missing-column branch; strict M0 is independently enforced | Same serialized bytes return ordinary NULL but fail strict unauthorized interpretation, including empty selection; not a new historical-upgrade certificate |
| Recovery and clone lose semantics | Contract propagation/rebinding, cached-plan invalidation and authoritative owner checks | Real WAL-only/checkpoint-WAL restart, old snapshot and sibling isolation; no blanket physical-restore support |
| Remote state lost or cross-member deletes misapplied | Fixed statement view and supported actual dispatch chain, ordinary combined controls retained | Real two-CN SQL and race; not a general distributed executor redesign |
| Repeated validation cost | Bounded per-read layout, existing metadata reuse, no global cache; complete declared-prefix validation | No performance qualification; O(B*C) validation remains an explicit cost to assess separately if needed |

## 5. Validation and evidence reuse

| Evidence | Result | Interpretation |
|---|---|---|
| M1 final SQL | 13 roots plus separate sibling scenario passed | Real issuance, ADD, COPY, old objects, four rollback windows, clone/merge and both restart modes; unchanged closures reused |
| M2 final tests | 25 default / 61 M0 root invocations passed | Includes actual two-CN capture through RPC/read and finite consumer checks; not external BVT |
| M3 final tests/compile/vet | 64 default / 102 M0 root invocations; 17 owning packages | Compile-only packages explicitly separated, not counted as test execution |
| M3 race | 15 mapping/owner roots x100; 3 SQL roots x1; 2 backup roots x1, all passed | Scoped ownership evidence, not a stability matrix |
| Static | Two-mode incremental golangci-lint and molint; gofmt, err-check, diff-check passed | Bound to sealed code; documentation-only additions did not alter compiled code |

Primary receipts and selections:

- [ar1-m3-final-source.json](attachments/ar1-m3-final-source.json)
- [ar1-m3-code-validation-source.json](attachments/ar1-m3-code-validation-source.json)
- [ar1-m3-final-checks.json](attachments/ar1-m3-final-checks.json)
- [ar1-m3-final-lint-checks.json](attachments/ar1-m3-final-lint-checks.json)
- [ar1-m3-final-molint-checks.json](attachments/ar1-m3-final-molint-checks.json)
- [ar1-m2-final-source.json](attachments/ar1-m2-final-source.json)
- [ar1-m1-current-source.json](attachments/ar1-m1-current-source.json)

These files include exact package/tag/run selections and log hashes; use them rather than inventing a broad rerun. Go 1.27.1 and matching native provenance are required to reproduce. The attached JSON receipts are exact byte copies; terminal results are safe structured extractions bound to original log hashes. Raw logs, host details and the historical working journal remain local. The attachment summary does not replace an independent execution or complete semantic review.

Historical failures are preserved. M3's changed-during-compilation tagged trial is not final evidence. Default evidence is reused only because its subsequent fixture edit was excluded by the mode. Final tagged and race runs were source-frozen. Generated-column controls now require COPY, not expanded M0 support. Author-reported fixes and test PASS do not substitute for independent review.

## 6. Remaining qualification and requested review output

| Item | Status for this candidate |
|---|---|
| Local M1/M2/M3 | PASS_LOCAL, final committed tree matches sealed content |
| Independent complete-candidate review | PENDING; this package requests it, does not perform it |
| Maintainer architecture acceptance | NOT_OBTAINED by these local results |
| Linux correctness qualification | NOT_RUN; preparation is not execution |
| External target BVT / exact published-head CI | NOT_RUN for this candidate |
| QA / production / mixed-version / downgrade | Not qualified |
| New performance result | NOT_RUN / no claim; CF9 approximate 27% observation remains historical |

Please return: (1) architecture decisions on persisted authority, Engine/ObjectIO separation and default policy; (2) verified blocking findings with file/line, reachable state, failed invariant and smallest corrective scope; (3) any objectively missing validation tied to a specific invariant; (4) non-blocking follow-ups separately. Do not infer approval from the local status. No outcome should automatically resolve GitHub threads or change PR state.

The review covers this complete four-commit candidate and its interaction with inherited code. It does not request performance tuning, SF100, schema expansion, a provenance ledger or a general distributed scan framework. Linux and CI may be completed separately, but their pending status must remain visible in any final merge/QA decision.

## 7. Maintainer-facing handoff text

> The AR-1 candidate is published as `61d76463baf34090c16fe94669aa4dd0fa4655ad` (four commits from `667fde44`). It introduces an explicit persisted relation evolution contract, moves missing-column interpretation to Engine-owned read layouts, closes the supported remote statement-state path, and restores default ordinary read compatibility while retiring ObjectIO M0 policy. The final tree matches the locally validated snapshot. We request architecture and implementation review of the complete candidate, especially persistent metadata compatibility, owner/snapshot binding, strict/default separation and lifecycle correctness. Local default/M0, real SQL and race evidence is attached by exact identity; Linux, external BVT, published-head CI and QA remain pending. No new performance benefit or production/mixed-version eligibility is claimed. Source links are pinned to the published candidate; attached receipts remain author-reported local evidence, not Linux/CI qualification.
