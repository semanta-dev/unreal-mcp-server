<#
.SYNOPSIS
  Renders deploy/mcp.json.tmpl into a consuming project's .mcp.json.
.EXAMPLE
  .\scripts\render-mcp-config.ps1 -ProjectDir C:\games\MyGame -EngineDir D:\Unreal\Engine\UE_5.7
  Writes C:\games\MyGame\.mcp.json pointing at this repo's dist\unreal-mcp.exe.
.PARAMETER Out
  Output path (default: <ProjectDir>\.mcp.json). Use -WhatIf to print instead of writing.
#>
[CmdletBinding(SupportsShouldProcess)]
param(
  [Parameter(Mandatory)][string]$ProjectDir,
  [string]$EngineDir = $env:UMCP_ENGINE_DIR,
  [string]$Exe,
  [string]$Out
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
if (-not $EngineDir) { throw 'EngineDir is required (pass -EngineDir or set UMCP_ENGINE_DIR).' }
if (-not $Exe) { $Exe = Join-Path $repo 'dist\unreal-mcp.exe' }
if (-not $Out) { $Out = Join-Path $ProjectDir '.mcp.json' }

$norm = { param($p) ($p -replace '\\', '/') }
$json = (Get-Content (Join-Path $repo 'deploy\mcp.json.tmpl') -Raw).
  Replace('${UMCP_EXE}', (& $norm $Exe)).
  Replace('${UMCP_PROJECT_DIR}', (& $norm $ProjectDir)).
  Replace('${UMCP_ENGINE_DIR}', (& $norm $EngineDir))

if ($PSCmdlet.ShouldProcess($Out, 'write .mcp.json')) {
  Set-Content -Path $Out -Value $json -Encoding utf8
  Write-Host "[render] wrote $Out"
} else {
  $json
}
