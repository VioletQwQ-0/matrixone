import json, pathlib, sys
events, summary, role, status = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), sys.argv[3], int(sys.argv[4])
records = [json.loads(line) for line in events.read_text().splitlines() if line.startswith('{')]
name = 'TestPR' + role + 'FrozenCanonicalBVT'
passes = [r for r in records if r.get('Action') == 'pass' and r.get('Test') == name]
package_passes = [r for r in records if r.get('Action') == 'pass' and not r.get('Test')]
if status != 0 or len(passes) != 1 or len(package_passes) != 1 or not summary.exists():
    raise SystemExit('FAIL_OR_BLOCKED: canonical test did not complete')
d = json.loads(summary.read_text())
runs = d.get('runs', [])
expected = 3
if d.get('metadata_comparison') is not True or d.get('failed') is not False or len(runs) != expected or any(r.get('status') != 'PASS' for r in runs):
    raise SystemExit('INCONCLUSIVE: canonical run count or per-run result mismatch')
if not d.get('cluster_closed') or not d.get('fixture_listeners_closed') or not d.get('owned_input_staging_removed'):
    raise SystemExit('INCONCLUSIVE: fixture cleanup not verified')
print('PASS: canonical comparator, expected repetitions and isolated fixture cleanup')
