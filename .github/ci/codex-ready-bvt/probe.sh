#!/usr/bin/env bash
set -euo pipefail
role="$1"; expected="$2"; tree="$3"
cd /go/src/github.com/matrixorigin/matrixone
git config --global --add safe.directory "$PWD"
actual="$(git rev-parse HEAD)"
printf 'pr=%s\nexpected_head=%s\nactual_head=%s\nexpected_tree=%s\n' "$role" "$expected" "$actual" "$tree" > /out/identity.txt
test "$actual" = "$expected"
sha256sum "/control/$role.patch" "/control/${role}_test.go" > /out/inputs.sha256
git apply --index "/control/$role.patch"
actual_tree="$(git write-tree)"
printf 'actual_tree=%s\n' "$actual_tree" >> /out/identity.txt
test "$actual_tree" = "$tree"
git diff --cached --check > /out/diff-check.log
go version >> /out/identity.txt
uname -sm >> /out/identity.txt
if test -x ./optools/images/ci-builder-fingerprint.sh && test -r /mo-prebuilt/thirdparties.fingerprint; then
  ./optools/images/ci-builder-fingerprint.sh > /out/expected-native.fingerprint
  cp /mo-prebuilt/thirdparties.fingerprint /out/image-native.fingerprint
  if cmp -s /out/expected-native.fingerprint /out/image-native.fingerprint; then
    cp -a /mo-prebuilt/thirdparties/install thirdparties/install
    printf 'matching prebuilt thirdparties\n' > /out/native-mode.txt
  else printf 'native fingerprint differs\n' > /out/native-mode.txt; fi
else printf 'native fingerprint unavailable\n' > /out/native-mode.txt; fi
if ! command -v python3 >/dev/null 2>&1; then
  if ! apt-get update -qq > /out/python-install.log 2>&1 || ! DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends python3 >> /out/python-install.log 2>&1; then
    echo 'BLOCKED installing Python in isolated container' > /out/result.txt
    exit 2
  fi
fi
python3 --version > /out/python-version.txt
export LANG=C.UTF-8 LC_ALL=C.UTF-8 JAVA_TOOL_OPTIONS=-Dfile.encoding=UTF-8
/jdk/bin/java -XshowSettings:properties -version > /out/java-settings.log 2>&1
grep -Eq 'file.encoding[[:space:]]*=[[:space:]]*UTF-8' /out/java-settings.log
export GOWORK=off GOMAXPROCS=2 GOMEMLIMIT=6GiB CGO_ENABLED=1
if ! make -j2 cgo > /out/native-build.log 2>&1; then echo 'BLOCKED native build' > /out/result.txt; exit 2; fi
export MO_MAINTENANCE_EVIDENCE=/out/bvt MO_MAINTENANCE_SERVICE_LOG=/out/test.jsonl
export MO_MAINTENANCE_CASES="$PWD/test/distributed/cases/dtype/binary_string_function_semantics.test"
python3 -c 'import json,sys; print(json.dumps({"Replace":{"/go/src/github.com/matrixorigin/matrixone/pkg/embed/codex_pr_bvt_test.go":"/control/"+sys.argv[1]+"_test.go"}}))' "$role" > /out/overlay.json
set +e
.agents/skills/mo-dev/scripts/mo-cgo-test -mod=readonly -json -count=1 -p=1 -parallel=1 -timeout=25m -overlay=/out/overlay.json -run "^TestPR${role}FrozenCanonicalBVT$" ./pkg/embed > /out/test.jsonl 2> /out/test.stderr
status=$?
set -e
python3 /control/verify.py /out/test.jsonl /out/bvt/canonical-summary.json "$role" "$status" > /out/result.txt
test "$(git write-tree)" = "$tree"
git diff --quiet
printf 'tree and tracked source unchanged after tests\n' > /out/final-integrity.txt
