import json, pathlib, sys
p, summary, status = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), int(sys.argv[3])
try:
    events = [json.loads(line) for line in p.read_text().splitlines() if line.startswith('{')]
    data = json.loads(summary.read_text())
except Exception as exc:
    raise SystemExit('INCONCLUSIVE: missing or invalid evidence: ' + str(exc))
name = 'TestPR31460FrozenCanonicalBVT'
runs = data.get('runs', [])
checks = [status == 0, len([e for e in events if e.get('Action')=='pass' and e.get('Test')==name])==1, len([e for e in events if e.get('Action')=='pass' and not e.get('Test')])==1, data.get('failed') is False, data.get('skip_check_user') is False, data.get('metadata_comparison') is True, data.get('cluster_closed') is True, data.get('sql_listener_closed') is True, data.get('owned_input_staging_removed') is True, len(runs)==2, all(r.get('pass') is True for r in runs)]
if not all(checks):
    raise SystemExit('FAIL_OR_INCONCLUSIVE: see retained test, parser, report and summary evidence')
print('PASS: 2 canonical comparisons, independent postconditions and fixture shutdown')
