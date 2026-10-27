#!/usr/bin/env bash
set -euo pipefail
cd /go/src/github.com/matrixorigin/matrixone
git config --global --add safe.directory "$PWD"
head="$(git rev-parse HEAD)"
printf 'head=%s\n' "$head" > /out/identity.txt
test "$head" = 8e65f80ff74ad00fa6e2b9ebedb7ebc8368a1e75
git apply --index /control/29057.patch
test "$(git write-tree)" = e4fff3db15f7397769a2f2dd70f65a256d4342d1
git diff --cached --check > /out/diff-check.log
if ! git cat-file -e c4946bb74d43f0f777977507a5a7d0105e628bf2^{commit} 2>/dev/null; then
  git fetch --no-tags --no-write-fetch-head https://github.com/matrixorigin/matrixone.git c4946bb74d43f0f777977507a5a7d0105e628bf2 > /out/base-fetch.log 2>&1
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
packages=(./pkg/sql/plan)
set +e
go vet -all -mod=readonly -p=1 "${packages[@]}" > /out/vet.log 2>&1
vet=$?
mkdir -p /tmp/mo-static-bin
GOBIN=/tmp/mo-static-bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 > /out/lint-install.log 2>&1
install=$?
lint=125
if [ "$install" = 0 ]; then
  /tmp/mo-static-bin/golangci-lint run -c .golangci.yml --concurrency=1 --timeout=20m --new-from-rev 8e65f80ff74ad00fa6e2b9ebedb7ebc8368a1e75 "${packages[@]}" > /out/lint.log 2>&1
  lint=$?
fi
.agents/skills/mo-dev/scripts/mo-cgo-test -json -p=1 -count=1 -timeout=25m "${packages[@]}" > /out/owning.jsonl 2> /out/owning.stderr
owning=$?
set -e
printf 'vet=%s\ninstall=%s\nlint=%s\nowning=%s\n' "$vet" "$install" "$lint" "$owning" > /out/result.txt
if [ "$vet" != 0 ] || [ "$install" != 0 ] || [ "$lint" != 0 ] || [ "$owning" != 0 ]; then exit 1; fi
