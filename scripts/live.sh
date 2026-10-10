#!/usr/bin/env bash
# Live tests (internal/livetest): the real server against running Unreal editors on
# scratch copies of the target games. Never point these at a real project: the tests
# play, spawn, create assets under /Game/LiveTest and place roads.
#
#   bash scripts/live.sh                 # every live test
#   bash scripts/live.sh -run Aim        # a subset (go test -run)
#
# Defaults are this machine's scratch copies; override with the environment.
set -euo pipefail
cd "$(dirname "$0")/.."
SCRATCH="${UMCP_LIVE_SCRATCH:-C:/Users/jorda/code/games/_p7scratch}"
export UMCP_LIVE_AESIR="${UMCP_LIVE_AESIR:-$SCRATCH/aesir}"
export UMCP_LIVE_AESIR_ADDR="${UMCP_LIVE_AESIR_ADDR:-127.0.0.1:6791}"
export UMCP_LIVE_POLYWORLD="${UMCP_LIVE_POLYWORLD:-$SCRATCH/PolyWorld}"
export UMCP_LIVE_POLYWORLD_ADDR="${UMCP_LIVE_POLYWORLD_ADDR:-127.0.0.1:6792}"
export UMCP_LIVE_GROUP="${UMCP_LIVE_GROUP:-239.0.0.42:6799}"
export UMCP_ENGINE_DIR="${UMCP_ENGINE_DIR:-D:/Unreal/Engine/UE_5.7}"
exec go test -tags live -count=1 -v -timeout 60m ./internal/livetest/ "$@"
