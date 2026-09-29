#!/usr/bin/env bash
# Self-test for scripts/coverage.sh: generated code is filtered out, and the
# gate passes below / fails above the measured total. Needs `go` (go tool cover).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/raw.out" <<'P'
mode: set
github.com/fclairamb/solidping/server/internal/version/version.go:1.1,2.2 4 1
github.com/fclairamb/solidping/server/internal/version/version.go:3.1,4.2 4 0
github.com/fclairamb/solidping/server/pkg/client/client_generated.go:1.1,2.2 100 0
github.com/fclairamb/solidping/server/third_party/grdp/x.go:1.1,2.2 100 0
github.com/fclairamb/solidping/server/internal/b/mock_thing.go:1.1,2.2 100 0
P

"$here/coverage.sh" filter "$tmp/raw.out" "$tmp/f.out"
if grep -qE 'pkg/client|third_party|mock_' "$tmp/f.out"; then
  echo "FAIL: generated paths survived the filter" >&2; exit 1
fi
# A real (tiny) profile for the total and the gate.
(cd "$here/../server" && go test -count=1 -coverprofile="$tmp/real.out" ./internal/version >/dev/null)
total="$(cd "$here/../server" && "$here/coverage.sh" total "$tmp/real.out")"
[[ "$total" =~ ^[0-9]+(\.[0-9]+)?$ ]] || { echo "FAIL: total not a number: $total" >&2; exit 1; }

(cd "$here/../server" && COVERAGE_MIN=0 "$here/coverage.sh" gate "$tmp/real.out" >/dev/null) || { echo "FAIL: gate should pass" >&2; exit 1; }
if out="$(cd "$here/../server" && COVERAGE_MIN=101 "$here/coverage.sh" gate "$tmp/real.out" 2>&1)"; then
  echo "FAIL: gate should fail at 101" >&2; exit 1
fi
grep -q "${total}%" <<<"$out" && grep -q '101%' <<<"$out" || { echo "FAIL: gate output lacks total/threshold" >&2; exit 1; }

# badge: colour from the total vs COVERAGE_MIN=80 (green >= 80, yellow >= 75, red below).
# A throwaway module with a 100-statement function, so blocks can be marked
# covered/uncovered at exact percentages.
mod="$tmp/mod"; mkdir -p "$mod"
printf 'module example.com/cov\n\ngo 1.21\n' > "$mod/go.mod"
{ echo 'package cov'; echo 'func F() {'; for _ in $(seq 100); do echo '	_ = 1'; done; echo '}'; } > "$mod/f.go"
mkprof() { # <covered> <uncovered> <file>: lines 3..102 are one statement each
  {
    echo 'mode: set'
    [[ "$1" -eq 0 ]] || echo "example.com/cov/f.go:3.1,$((2 + $1)).8 $1 1"
    [[ "$2" -eq 0 ]] || echo "example.com/cov/f.go:$((3 + $1)).1,$((2 + $1 + $2)).8 $2 0"
  } > "$3"
}
check_badge() { # <covered> <uncovered> <expected total> <colour>
  mkprof "$1" "$2" "$tmp/b.out"
  json="$(cd "$mod" && COVERAGE_MIN=80 "$here/coverage.sh" badge "$tmp/b.out")"
  python3 - "$json" "$3" "$4" <<'PY' || { echo "FAIL: badge $3/$4: $json" >&2; exit 1; }
import json, sys
d = json.loads(sys.argv[1])
assert d["schemaVersion"] == 1 and d["label"] == "coverage", d
assert d["message"] == sys.argv[2] + "%", d
assert d["color"] == sys.argv[3], d
PY
}
check_badge 85 15 "85.0" brightgreen
check_badge 78 22 "78.0" yellow
check_badge 60 40 "60.0" red

# badge: a missing or empty profile must fail and print no JSON.
for f in "$tmp/nope.out" "$tmp/empty.out"; do
  : > "$tmp/empty.out"
  if out="$(cd "$here/../server" && "$here/coverage.sh" badge "$f" 2>/dev/null)"; then
    echo "FAIL: badge should fail on $f" >&2; exit 1
  fi
  [[ -z "$out" ]] || { echo "FAIL: badge printed output on $f: $out" >&2; exit 1; }
done
echo "coverage.sh self-test ok"
