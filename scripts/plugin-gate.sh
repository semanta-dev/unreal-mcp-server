#!/usr/bin/env bash
# The plugin rebuild gate (docs/plans/REMEDIATION_PLAN.md §7): GitHub's runners have no
# UE 5.7, so every change to plugin/UnrealMCP is compiled into a SCRATCH copy of a game
# project on a workstation and checked live before it merges.
#
#   scripts/plugin-gate.sh <project dir> <expected plugin API> [log file]
#
# Copies plugin/UnrealMCP into <project>/Plugins, builds it with the server's own
# `build strategy=ubt` (safe editor shutdown, Build.bat, relaunch on the same map) and
# checks `editor op=health expect_plugin=<N>`. Environment: UMCP_ENGINE (default
# D:/Unreal/Engine/UE_5.7), UMCP_GROUP (the scratch copy's multicast group, default
# 239.0.0.42:6799), UMCP_COMMAND_ADDR (default 127.0.0.1:6791). Exit 0 only when the
# build succeeded and the editor reports the expected API.
set -euo pipefail
cd "$(dirname "$0")/.."
proj="${1:?usage: plugin-gate.sh <project dir> <expected plugin API> [log file]}"
api="${2:?expected plugin API}"
log="${3:-/dev/stderr}"
engine="${UMCP_ENGINE:-D:/Unreal/Engine/UE_5.7}"
group="${UMCP_GROUP:-239.0.0.42:6799}"
addr="${UMCP_COMMAND_ADDR:-127.0.0.1:6791}"

go build -o dist/unreal-mcp.exe ./cmd/unreal-mcp
go build -o dist/mcpcall.exe ./cmd/mcpcall
mkdir -p "$proj/Plugins/UnrealMCP"
# Source and descriptor only: the project's Binaries/Intermediate are rebuilt by UBT.
rm -rf "$proj/Plugins/UnrealMCP/Source"
cp -r plugin/UnrealMCP/Source plugin/UnrealMCP/UnrealMCP.uplugin "$proj/Plugins/UnrealMCP/"

calls="$(mktemp)"
trap 'rm -f "$calls"' EXIT
# Build BEFORE opening: an editor launched on new plugin source with old binaries stops at
# a "Missing <Project> Modules — rebuild?" prompt. build strategy=ubt closes a running
# editor safely (or builds directly when none runs); ensure_open then (re)launches it.
{
  echo '{"tool":"build","args":{"strategy":"ubt","wait_s":25}}'
  # A build outlives one call: keep waiting (a finished job answers at once) — never
  # exit mid-build, which would leave the editor closed.
  for _ in $(seq 1 30); do echo '{"tool":"job","args":{"op":"wait","job_id":"job-1","wait_s":25}}'; done
  echo '{"tool":"editor_lifecycle","args":{"op":"ensure_open","wait_s":25}}'
  for _ in $(seq 1 24); do echo '{"tool":"job","args":{"op":"wait","job_id":"job-2","wait_s":25}}'; done
  echo "{\"tool\":\"editor\",\"args\":{\"op\":\"health\",\"expect_plugin\":$api}}"
} > "$calls"
out="$(dist/mcpcall.exe -timeout 30m -- dist/unreal-mcp.exe -project "$proj" -engine "$engine" -group "$group" \
  -command-addr "$addr" -log-format text -log-level warn < "$calls" 2>&1)"
printf '%s\n' "$out" >> "$log"
health="$(printf '%s\n' "$out" | grep '"tool":"editor"' | tail -1)"
if printf '%s' "$health" | grep -q '"healthy":true' && printf '%s' "$health" | grep -q "\"plugin_api\":$api[,}]"; then
  echo "[plugin-gate] PASS: $proj reports plugin API $api"
else
  echo "[plugin-gate] FAIL: $health" >&2
  exit 1
fi
