<#
.SYNOPSIS
  Re-derives the v1 baseline code references cited in docs/plans/OVERHAUL_PLAN.md §0
  from the v1-final tag (the baseline is history: the overhaul changes these files),
  so the plan's file:line claims stay checkable. Exits non-zero on a missing anchor.
#>
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
$tag = 'v1-final'

$anchors = @(
  @{ File = 'internal/tools/register.go';            Pattern = '^func RegisterAll' },
  @{ File = 'internal/daemonwire/project_tools.go';  Pattern = 'Name:\s+"project_(attach|release|list)"' },
  @{ File = 'internal/snippets/py/mcp_bridge.py';    Pattern = '^def _mcp_dispatch\(' },
  @{ File = 'internal/snippets/py/mcp_bridge.py';    Pattern = 'result.get\("error"\) and "code" not in result' },
  @{ File = 'internal/snippets/py/mcp_bridge.py';    Pattern = '^def _pick_world' },
  @{ File = 'internal/snippets/py/mcp_bridge.py';    Pattern = '^_OPS\s*=\s*\{' },
  @{ File = 'internal/tools/build_tools.go';         Pattern = 'add\(s, "editor_restart"' },
  @{ File = 'internal/tools/discovery_tools.go';     Pattern = 'add\(s, "project_map"' },
  @{ File = 'internal/cockpitbridge/bootstrap.go';   Pattern = 'func \(.*MemEpochStore\) LastSeq' },
  @{ File = 'cmd/unreal-mcp/daemon.go';              Pattern = 'mcp.NewServer|NewStreamableHTTPHandler' },
  @{ File = 'internal/uexec/client.go';              Pattern = 'errors.Is\(err, ErrConnectionLost\) && attempt == 0' },
  @{ File = 'internal/uexec/command.go';             Pattern = 'ErrConnectionLost' },
  @{ File = 'internal/uexec/config.go';              Pattern = 'defaultCommandAddr\s*=' },
  @{ File = 'internal/bridge/install.go';            Pattern = 'cur != snippets.Version\(\)' },
  @{ File = 'internal/fakeeditor/fakeeditor.go';     Pattern = 'case "open_connection"|case "close_connection"' }
)

$failed = 0
foreach ($a in $anchors) {
  $content = git show "${tag}:$($a.File)" 2>$null
  if ($LASTEXITCODE -ne 0 -or -not $content) { "MISSING FILE  ${tag}:$($a.File)"; $failed++; continue }
  $lines = $content -split "`n"
  $hit = $false
  for ($i = 0; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match $a.Pattern) { "{0}:{1}  {2}" -f $a.File, ($i + 1), $lines[$i].Trim(); $hit = $true }
  }
  if (-not $hit) { "NO MATCH      $($a.File)  /$($a.Pattern)/"; $failed++ }
}

$ops = git show "${tag}:internal/snippets/py/mcp_bridge.py" | Out-String
if ($ops -match '(?s)\n_OPS\s*=\s*\{(.*?)\n\}') {
  $n = ([regex]::Matches($Matches[1], '(?m)^\s*"[a-z_]+"\s*:')).Count
  "_OPS literal entries at ${tag}: $n"
}
if ($failed -gt 0) { Write-Error "$failed anchor(s) missing"; exit 1 }
