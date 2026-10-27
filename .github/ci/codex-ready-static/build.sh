#!/usr/bin/env bash
set -euo pipefail
cd /subject
git config --global --add safe.directory /subject
test "$(git rev-parse HEAD)" = 536b2aa3e5f130691492822c477cbbf872fae0e6
test "$(sha256sum /control/28935.patch | cut -d' ' -f1)" = e7cd0442ceb30f6afd27461663e43a2dca7f5171e03566cdefaace97505bc8cc
git apply --index /control/28935.patch
test "$(git write-tree)" = 9d78d2089031ec23e23697a8bd6fb9c4c91d8f00
git diff --cached --check 536b2aa3e5f130691492822c477cbbf872fae0e6
if test -x ./optools/images/ci-builder-fingerprint.sh && test -r /mo-prebuilt/thirdparties.fingerprint; then
  ./optools/images/ci-builder-fingerprint.sh > /out/expected-native.fingerprint
  cp /mo-prebuilt/thirdparties.fingerprint /out/image-native.fingerprint
  if cmp -s /out/expected-native.fingerprint /out/image-native.fingerprint; then cp -a /mo-prebuilt/thirdparties/install thirdparties/install; fi
fi
export GOWORK=off GOMAXPROCS=2 GOMEMLIMIT=8GiB CGO_ENABLED=1
make -j2 cgo
make build-with-prebuilt-native BRANCH_NAME=pr28935-candidate LAST_COMMIT_ID=536b2aa3e5f130691492822c477cbbf872fae0e6
