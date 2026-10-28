#!/usr/bin/env bash
set -euo pipefail
cd /go/src/github.com/matrixorigin/matrixone
git config --global --add safe.directory "$PWD"
head="$(git rev-parse HEAD)"
printf 'head=%s\ntree=%s\n' "$head" "$(git rev-parse HEAD^{tree})" > /out/identity.txt
test "$head" = 81bcb78a7872f92c48f13503261abcc119a54caf
test "$(git rev-parse HEAD^{tree})" = 20e6c86aea997f6225cb0a7d8c8ed059f0f2969b
git diff --check > /out/diff-check.log
if test -x ./optools/images/ci-builder-fingerprint.sh && test -r /mo-prebuilt/thirdparties.fingerprint; then
  ./optools/images/ci-builder-fingerprint.sh > /out/expected-native.fingerprint
  cp /mo-prebuilt/thirdparties.fingerprint /out/image-native.fingerprint
  if cmp -s /out/expected-native.fingerprint /out/image-native.fingerprint; then cp -a /mo-prebuilt/thirdparties/install thirdparties/install; fi
fi
export GOWORK=off GOMAXPROCS=2 GOMEMLIMIT=6GiB CGO_ENABLED=1
if ! make -j2 cgo > /out/native-build.log 2>&1; then echo 'BLOCKED native build' > /out/result.txt; exit 2; fi
export CGO_CFLAGS="-I$PWD/cgo -I$PWD/thirdparties/install/include"
export CGO_LDFLAGS="-L$PWD/thirdparties/install/lib"
export LD_LIBRARY_PATH="$PWD/cgo:$PWD/thirdparties/install/lib"
printf '%s\n' '{"Replace":{"/go/src/github.com/matrixorigin/matrixone/pkg/embed/codex_pr_snapshot_test.go":"/control/snapshot_test.go"}}' > /out/overlay.json
set +e
.agents/skills/mo-dev/scripts/mo-cgo-test -mod=readonly -json -p=1 -count=1 -timeout=20m -overlay=/out/overlay.json -run '^TestPR31460SnapshotAndSQLPrepare$' ./pkg/embed > /out/tests.jsonl 2> /out/tests.stderr
status=$?
set -e
printf 'tests=%s\n' "$status" > /out/result.txt
exit "$status"
