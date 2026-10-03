#!/bin/bash
# run.sh <aesir|PolyWorld> <calls.jsonl> [extra server flags]
P=$1; F=$2; shift 2
R=/c/Users/jorda/code/games/unreal-mcp-server
D=/c/Users/jorda/AppData/Local/Temp/claude/C--Users-jorda-code-games-unreal-mcp-server/de8c76ec-2c83-4a87-a5c0-9a34f7bcedbf/scratchpad/t4
case $P in aesir) PORT=6791;; *) PORT=6792;; esac
$R/dist/mcpcall.exe -images $D/img -- $R/dist/unreal-mcp.exe -project C:/Users/jorda/code/games/_p7scratch/$P \
  -engine D:/Unreal/Engine/UE_5.7 -group 239.0.0.42:6799 -command-addr 127.0.0.1:$PORT -log-format text -log-level warn "$@" < $F
