#!/usr/bin/env bash
set -euo pipefail
cd /go/src/github.com/matrixorigin/matrixone
git config --global --add safe.directory "$PWD"
head="$(git rev-parse HEAD)"
printf 'head=%s\n' "$head" > /out/identity.txt
test "$head" = 823dc248403520744a6ea9d777cc065fe43910ff
git apply --index /control/31460.patch
test "$(git write-tree)" = b77082467c1983c072e6102c490d2bcf6431e490
git diff --cached --check > /out/diff-check.log
if ! git cat-file -e 3b7752551b8a1cc60daddecde9a5438d5357cdf4^{commit} 2>/dev/null; then
  git fetch --no-tags --no-write-fetch-head https://github.com/matrixorigin/matrixone.git 3b7752551b8a1cc60daddecde9a5438d5357cdf4 > /out/base-fetch.log 2>&1
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
.agents/skills/mo-dev/scripts/mo-cgo-test -json -p=1 -count=1 -timeout=25m -run '^(TestMigrate.*|TestQueryServiceMigrate.*|TestCanceledMigrationAdmission.*|TestRenameUserIdentity|TestProxyRenameUserMigrationRejectsChangedPrincipal)$' ./pkg/frontend ./pkg/proxy ./pkg/embed > /out/tests.jsonl 2> /out/tests.stderr
tests=$?
go vet -all -mod=readonly -p=1 ./pkg/frontend ./pkg/proxy ./pkg/embed ./pkg/pb/query ./pkg/defines > /out/vet.log 2>&1
vet=$?
race=125
if [ "$tests" = 0 ]; then
 .agents/skills/mo-dev/scripts/mo-cgo-test -race -json -p=1 -count=1 -timeout=25m -run '^TestProxyRenameUserMigrationRejectsChangedPrincipal$' ./pkg/proxy > /out/race.jsonl 2> /out/race.stderr
 race=$?
fi
set -e
printf 'tests=%s\nvet=%s\nrace=%s\n' "$tests" "$vet" "$race" > /out/result.txt
if [ "$tests" != 0 ] || [ "$vet" != 0 ] || [ "$race" != 0 ]; then exit 1; fi
