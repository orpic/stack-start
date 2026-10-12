#!/bin/sh
# Checks the runner itself: every must-pass case passes and every must-fail
# case fails. Usage: conformance/runner/selftest/run.sh <runner> <stackstart>
set -u
runner=$1 bin=$2
dir=$(cd "$(dirname "$0")" && pwd)
status=0
"$runner" --bin "$bin" --cases "$dir/must-pass" || status=1
for c in "$dir"/must-fail/*/; do
  name=$(basename "$c")
  if "$runner" --bin "$bin" --cases "$dir/must-fail" --filter "$name" >/dev/null 2>&1; then
    echo "SELFTEST FAIL: must-fail/$name passed"
    status=1
  else
    echo "selftest ok: must-fail/$name fails"
  fi
done
exit $status
