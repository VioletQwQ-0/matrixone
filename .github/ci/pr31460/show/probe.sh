#!/usr/bin/env bash
set -euo pipefail
cd /go/src/github.com/matrixorigin/matrixone
git config --global --add safe.directory "$PWD"
test "$(git rev-parse HEAD)" = 81bcb78a7872f92c48f13503261abcc119a54caf
test "$(git rev-parse HEAD^{tree})" = 20e6c86aea997f6225cb0a7d8c8ed059f0f2969b
printf 'head=%s\ntree=%s\ncase=%s\n' "$(git rev-parse HEAD)" "$(git rev-parse HEAD^{tree})" test/distributed/cases/dml/show/show.test > /out/identity.txt
export LANG=C.UTF-8 LC_ALL=C.UTF-8 JAVA_TOOL_OPTIONS=-Dfile.encoding=UTF-8
if test -x ./optools/images/ci-builder-fingerprint.sh && test -r /mo-prebuilt/thirdparties.fingerprint; then
 ./optools/images/ci-builder-fingerprint.sh > /out/expected-native.fingerprint
 cp /mo-prebuilt/thirdparties.fingerprint /out/image-native.fingerprint
 if cmp -s /out/expected-native.fingerprint /out/image-native.fingerprint; then cp -a /mo-prebuilt/thirdparties/install thirdparties/install; fi
fi
export GOWORK=off GOMAXPROCS=2 GOMEMLIMIT=6GiB CGO_ENABLED=1
if ! make -j2 cgo > /out/native-build.log 2>&1; then printf 'BLOCKED native build\n' > /out/result.txt; exit 2; fi
export CASE_PATH=test/distributed/cases/dml/show/show.test MO_MAINTENANCE_EVIDENCE=/out/bvt MO_MAINTENANCE_SERVICE_LOG=/out/test.jsonl
printf '%s\n' '{"Replace":{"/go/src/github.com/matrixorigin/matrixone/pkg/embed/codex_pr_legacy_test.go":"/control/legacy_test.go"}}' > /out/overlay.json
set +e
.agents/skills/mo-dev/scripts/mo-cgo-test -mod=readonly -json -count=1 -p=1 -parallel=1 -timeout=25m -overlay=/out/overlay.json -run '^TestPR31460LegacyGolden$' ./pkg/embed > /out/test.jsonl 2> /out/test.stderr
status=$?
set -e
printf 'test_exit=%s\n' "$status" > /out/result.txt
git diff --quiet
exit "$status"
