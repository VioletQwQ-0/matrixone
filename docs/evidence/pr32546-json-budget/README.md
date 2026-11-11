# PR #32546 JSON query accounting evidence

This evidence branch is separate from the Ready feature PR. The dependency head is [`7ac7f32`](https://github.com/matrixorigin/matrixone/commit/7ac7f329b85787076433bc256d7ab066ff5a1db4), based on main `995b843`; the combined source tested after the merge is [`ed02517`](https://github.com/VioletQwQ-0/matrixone/commit/ed025175c334b483913f87d9221b97dbf1750b26). Independent design review, new-head CI and QA remain pending.

The canonical `func_json_value.test` and `func_json_value_float.sql` runs on `ed02517` passed 153/153 and 8/8. Their respective case/result SHA-256 pairs are `e818a5428371071f530767cba3da80beede4878184d0b1965c0d50f999b5ace2` / `80f5e5078d612962fec04de4bd8e175334ed824fb960b78b854a8cf71c95027b` and `2d9ac2c8b685d98e53c4c59a19fc9e1d7160ac48ed1cf1fcfc0173d6760291cf` / `1b790a0d9a019ed05fddd4bfdba93d1bee244af72798d2fab6172f01bc1deb06`. They were run locally with the existing BVT runner; they are not CI results.

## Five-pair Ubuntu comparisons

Each summary contains every measured scenario, five paired ratios, per-arm median ns/op, B/op and allocs/op. Identity files record source trees, synthetic test commits, binary and harness hashes, CPU configuration and native library hash. All ratios are medians of five alternating AB/BA pairs against original baseline `4df268e`. The SQL measurement includes admission, copying, scanning, execution and result release; resident measurements exclude source publication.

| Comparison | Candidate | Cases | Worst paired median |
| --- | --- | ---: | ---: |
| [Final SQL](sql-main-summary.json) | `ed02517` | 30 | 1.0617x |
| [Legacy 64-row expression](resident-summary.json) | `b16ffdf` | 30 | 1.0644x |
| [Contract 64-row expression](resident-summary.json) | `b16ffdf` | 30 | 1.0162x |
| [Warm resident lookup, 4096 elements](resident-summary.json) | `b16ffdf` | 1 | 0.8299x |
| [Mutable fallback, 4096 elements](resident-summary.json) | `b16ffdf` | 1 | 9.7308x |

The mutable path retains complete validation. The earlier resident candidate and final source have identical reader execution code apart from a comment; the later main merge changed binary conversion and functional-index planning, so SQL was measured again on `ed02517`. The two-/seven-argument resident comparison covers the shared short-text domain and does not establish throughput for all target types.

Final SQL had zero process major faults and one global swap-in page in baseline pair 3. Omitting that pair leaves a worst four-pair median of 1.0636x; peak process RSS was 467368 KiB. Ten SQL rows interleaved unrelated runner output; each measurement was recovered before the next benchmark label. The prior 50-window resident/SQL run had zero global swap events and 107 process major faults across five windows; these process-wide counts include setup and cannot be located precisely within timed operations. Omitting affected pairs leaves worst medians of 1.0802x legacy-64 (3 pairs), 0.8532x warm (4 pairs) and 1.0162x contract-64 (5 pairs). Five-pair results are primary; these sensitivity checks do not prove that faults had no effect.

One first attempt at the final-main SQL replay exited during compilation with an empty log when the original filesystem had approximately 0.9 GiB free. The reason was not established. The successful replay used a task-owned directory on a roomier filesystem. An earlier `double free or corruption` in the isolation layer also remains unclassified; native short-path controls passed, but neither causation nor resolution has been established. Reviewers and QA must decide its disposition before merge.

Composite/extended output formatting currently admits a conservative temporary bound of `32 * encoded data bytes + 2304`, plus its separately charged output. Tight budgets can therefore reject before actual peak use reaches that bound. The bound and the separate Vector/ObjectIO row-mapping and lifecycle implementations remain subjects for independent design review.
