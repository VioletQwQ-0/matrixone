#!/usr/bin/env bash
set -euo pipefail
cd /go/src/github.com/matrixorigin/matrixone
git config --global --add safe.directory "$PWD"
head="$(git rev-parse HEAD)"
printf 'head=%s\n' "$head" > /out/identity.txt
test "$head" = 536b2aa3e5f130691492822c477cbbf872fae0e6
git apply --index /control/28935.patch
test "$(git write-tree)" = 23090de9c597432e6f98050a0bac7c60b868c44e
git diff --cached --check > /out/diff-check.log
if ! git cat-file -e 9cf8743ed7b6b4269f50179f1b09664a7582f4e7^{commit} 2>/dev/null; then
  git fetch --no-tags --no-write-fetch-head https://github.com/matrixorigin/matrixone.git 9cf8743ed7b6b4269f50179f1b09664a7582f4e7 > /out/base-fetch.log 2>&1
fi
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
set +e
.agents/skills/mo-dev/scripts/mo-cgo-test -json -p=1 -count=1 -timeout=25m -run '^(TestCRC32LegacyGeneratedDMLRequiresRebuild|TestCRC32LegacyGeneratedWriteGateAllowsVirtualColumns)$' ./pkg/sql/plan > /out/tests.jsonl 2> /out/tests.stderr
tests=$?
go vet -all -mod=readonly -p=1 ./pkg/sql/plan > /out/vet.log 2>&1
vet=$?
set -e
printf 'tests=%s\nvet=%s\n' "$tests" "$vet" > /out/result.txt
if [ "$tests" != 0 ] || [ "$vet" != 0 ]; then exit 1; fi
