<#
.SYNOPSIS
  Re-derives the code references cited in docs/plans/OVERHAUL_PLAN.md §0 so the plan's
  file:line claims are checked against the tree, not trusted. Prints each anchor
  with its current line number; paste the output into docs/plans/OVERHAUL_PROGRESS.md.
#>
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$anchors = @(
  @{ File = 'internal/tools/register.go';            Pattern = '^func RegisterAll' },
  @{ File = 'internal/daemonwire/project_tools.go';  Pattern = 'Name:\s+"project_(attach|release|list)"' },
  @{ File = 'internal/bridge/py/99_dispatch.py';    Pattern = '^def _mcp_dispatch\(' },
  @{ File = 'internal/bridge/py/99_dispatch.py';    Pattern = 'result.get\("error"\) and "code" not in result' },
  @{ File = 'internal/bridge/py/40_observation.py'; Pattern = '^def _pick_world' },
  @{ File = 'internal/bridge/py/99_dispatch.py';    Pattern = '^_OPS\s*=\s*\{' },
  @{ File = 'internal/tools/build_tools.go';         Pattern = 'add\(s, "editor_restart"' },
  @{ File = 'internal/tools/discovery_tools.go';     Pattern = 'add\(s, "project_map"' },
  @{ File = 'internal/cockpit/attach/cockpitbridge_bootstrap.go';   Pattern = 'func \(.*MemEpochStore\) LastSeq' },
  @{ File = 'cmd/unreal-mcp/daemon.go';              Pattern = 'mcp.NewServer|NewStreamableHTTPHandler' },
  @{ File = 'internal/uexec/client.go';              Pattern = 'errors.Is\(err, ErrConnectionLost\) && attempt == 0' },
  @{ File = 'internal/uexec/command.go';             Pattern = 'ErrConnectionLost' },
  @{ File = 'internal/uexec/config.go';              Pattern = 'defaultCommandAddr\s*=' },
  @{ File = 'internal/bridge/install.go';            Pattern = 'cur != CompanionVersion\(\)' },
  @{ File = 'internal/uexec/uexectest/fakeeditor.go';     Pattern = 'case "open_connection"|case "close_connection"' }
)

$failed = 0
foreach ($a in $anchors) {
  if (-not (Test-Path $a.File)) { "MISSING FILE  $($a.File)"; $failed++; continue }
  $hits = Select-String -Path $a.File -Pattern $a.Pattern
  if (-not $hits) { "NO MATCH      $($a.File)  /$($a.Pattern)/"; $failed++; continue }
  foreach ($h in $hits) { "{0}:{1}  {2}" -f $a.File, $h.LineNumber, $h.Line.Trim() }
}

$ops = (Get-Content internal/bridge/py/99_dispatch.py -Raw)
if ($ops -match '(?s)\n_OPS\s*=\s*\{(.*?)\n\}') {
  $n = ([regex]::Matches($Matches[1], '(?m)^\s*"[a-z_]+"\s*:')).Count
  "_OPS literal entries: $n"
}
if ($failed -gt 0) { Write-Error "$failed anchor(s) missing"; exit 1 }
