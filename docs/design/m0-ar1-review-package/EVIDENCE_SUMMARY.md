# AR-1 local evidence summary

Candidate: `61d76463baf34090c16fe94669aa4dd0fa4655ad`.
Tree: `189f65182fba2b0a27a71f6a13faf3c5a1a10c4f`.
This is the current summary of the local working journal. The original journal
and raw logs are not republished. Their historical/failure hashes remain local;
the source manifest retains the original journal hash. No new test ran for publication.

| Item | Terminal result and boundary |
|---|---|
| M1 | Explicit persisted relation contract, controlled CREATE/COPY/ADD, clone, rollback and WAL/checkpoint local evidence; reused unchanged closures |
| M2 | Supported real two-CN statement-view capture/dispatch/read, relation-qualified data and deletion state, coordinator memory, ordinary partition controls and finite consumer map; scoped SQL race |
| M3 | ObjectIO semantic/context permission retired; Engine explicit strict layout; ordinary missing-column behavior restored, not a historical-upgrade certification |
| Final tests | 64 default and 102 mo_m0 root invocations; compile-only packages separately recorded; 17-package compile/vet closure |
| Race | 1500 mapping/lifetime, 3 real SQL, 2 backup root invocations passed |
| Static | Both modes incremental golangci-lint and molint, err-check, gofmt and diff-check passed |
| Source identity | 171 sealed files and eight deletions match final tree. Four commits only split history; intermediate commits were not independently compiled/qualified |
| External | Linux, external target BVT, published-head CI and QA NOT_RUN; independent complete review pending; no performance claim |

The receipt JSON files are exact copies. `attachments/TERMINAL_RESULTS.json`
extracts only root names/counts and binds original log hashes; it contains no
raw service output. A zero exit code or an author-reported PASS does not imply
maintainer approval. Negative trial logs and changed-during-compilation receipts
remain preserved locally and are not promoted to final evidence.

The original M1/M2 checkpoint prose may still describe the pre-retirement
implementation. Their identities stay historical; final M3 source/tests and
current R3 addendum describe explicit Engine handoff. Generated-column controls
require ordinary COPY; no generated-column M0 support is added. Backup fixtures
do not certify full physical backup/restore or cross-cluster publication.

Same-version isolated storage/deployment is required. Old/default binaries are
not guaranteed to reject experimental data. Persistent SchemaExtra/protobuf
semantics are new; disk-object encoding is unchanged. Catalog schema version
and contracted layout generation are separate. No object shape, writer ID or
column-width guess grants missing-column authority.
