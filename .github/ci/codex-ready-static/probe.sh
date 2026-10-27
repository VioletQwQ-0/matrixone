#!/usr/bin/env bash
set -euo pipefail
cd /go/src/github.com/matrixorigin/matrixone
git config --global --add safe.directory "$PWD"
test "$(git rev-parse HEAD)" = 9cf8743ed7b6b4269f50179f1b09664a7582f4e7
test "$(git rev-parse HEAD^{tree})" = a6b42a968083783957da0bdfd5568a566d99ae5b
printf 'head=%s\ntree=%s\n' "$(git rev-parse HEAD)" "$(git rev-parse HEAD^{tree})" > /out/identity.txt
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
cp /control/qa_28162_test.go pkg/embed/codex_qa_28162_test.go
set +e
.agents/skills/mo-dev/scripts/mo-cgo-test -json -p=1 -count=1 -timeout=15m -run '^TestIssue28162ExactMergedOriginalSQL$' ./pkg/embed > /out/test.jsonl 2> /out/test.stderr
result=$?
set -e
printf 'test_exit=%s\n' "$result" > /out/result.txt
exit "$result"
