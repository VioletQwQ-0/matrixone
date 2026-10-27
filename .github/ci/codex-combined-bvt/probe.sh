#!/usr/bin/env bash
set -euo pipefail
cd /go/src/github.com/matrixorigin/matrixone
git config --global --add safe.directory "$PWD"
printf 'expected_head=%s\nactual_head=%s\nexpected_tree=%s\nactual_tree=%s\n' 1395d1330e88ac9fccf83ccdd76d02f5b7747226 "$(git rev-parse HEAD)" e61bb2b826aac7005db91689815b68de2cf330b7 "$(git rev-parse HEAD^{tree})" > /out/identity.txt
test "$(git rev-parse HEAD)" = 1395d1330e88ac9fccf83ccdd76d02f5b7747226
test "$(git rev-parse HEAD^{tree})" = e61bb2b826aac7005db91689815b68de2cf330b7
sha256sum /control/31460_test.go /control/Inspect.java /control/probe.sh > /out/control-hashes.txt
git diff --check > /out/diff-check.log
actual_tree="$(git rev-parse HEAD^{tree})"
git status --short > /out/input-status.txt
go version >> /out/identity.txt
uname -sm >> /out/identity.txt
export LANG=C.UTF-8 LC_ALL=C.UTF-8 JAVA_TOOL_OPTIONS=-Dfile.encoding=UTF-8
mkdir -p /tmp/pr31460parse /out/parser
cp test/distributed/cases/zz_accesscontrol/rename_user.sql test/distributed/cases/zz_accesscontrol/rename_user.result /tmp/pr31460parse/
/jdk/bin/javac -cp '/tester/lib/*' -d /out/parser /control/Inspect.java > /out/parser-compile.log 2>&1
/jdk/bin/java -cp '/out/parser:/tester/lib/*' Inspect /tmp/pr31460parse/rename_user.sql > /out/parser.log 2>&1
if test -x ./optools/images/ci-builder-fingerprint.sh && test -r /mo-prebuilt/thirdparties.fingerprint; then
 ./optools/images/ci-builder-fingerprint.sh > /out/expected-native.fingerprint
 cp /mo-prebuilt/thirdparties.fingerprint /out/image-native.fingerprint
 if cmp -s /out/expected-native.fingerprint /out/image-native.fingerprint; then cp -a /mo-prebuilt/thirdparties/install thirdparties/install; printf 'matching prebuilt thirdparties\n' > /out/native-mode.txt; else printf 'native fingerprint differs\n' > /out/native-mode.txt; fi
fi
export GOWORK=off GOMAXPROCS=2 GOMEMLIMIT=6GiB CGO_ENABLED=1
if ! make -j2 cgo > /out/native-build.log 2>&1; then printf 'BLOCKED native build\n' > /out/result.txt; exit 2; fi
export MO_MAINTENANCE_EVIDENCE=/out/bvt MO_MAINTENANCE_SERVICE_LOG=/out/test.jsonl
printf '%s\n' '{"Replace":{"/go/src/github.com/matrixorigin/matrixone/pkg/embed/codex_pr_bvt_test.go":"/control/31460_test.go"}}' > /out/overlay.json
set +e
.agents/skills/mo-dev/scripts/mo-cgo-test -mod=readonly -json -count=1 -p=1 -parallel=1 -timeout=25m -overlay=/out/overlay.json -run '^TestPR31460FrozenCanonicalBVT$' ./pkg/embed > /out/test.jsonl 2> /out/test.stderr
status=$?
set -e
printf '%s\n' "$status" > /out/test-exit.txt
set +e
python3 /control/verify.py /out/test.jsonl /out/bvt/canonical-summary.json "$status" > /out/result.txt 2>&1
verification=$?
set -e
test "$(git write-tree)" = "$actual_tree"
git diff --quiet
printf 'candidate index and tracked source unchanged after test\n' > /out/final-integrity.txt
exit "$verification"
