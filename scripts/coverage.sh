#!/usr/bin/env bash
# Merged coverage gates (docs/plans/OVERHAUL_PLAN.md §1): every test in the repo
# counts toward each package's coverage (-coverpkg), so e2e scenarios cover the
# tools they drive. Fails when a package drops below its floor.
set -euo pipefail
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
fail=0

gate() { # name, coverpkg, floor
	local name=$1 pkg=$2 floor=$3
	go test ./... -count=1 -coverpkg="$pkg" -coverprofile="$out/$name.out" >/dev/null
	local pct
	pct=$(go tool cover -func="$out/$name.out" | awk '/^total:/ {sub("%", "", $NF); print $NF}')
	if awk -v p="$pct" -v f="$floor" 'BEGIN { exit !(p + 0 < f + 0) }'; then
		echo "coverage $name: $pct% < $floor%"
		fail=1
	else
		echo "coverage $name: $pct% (floor $floor%)"
	fi
}

gate repo   ./internal/...      75
gate tools  ./internal/tools    70
gate app    ./internal/app      80
gate config ./internal/config   90
exit $fail
