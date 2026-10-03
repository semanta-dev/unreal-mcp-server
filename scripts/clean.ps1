<#
.SYNOPSIS
  Prunes dist/ to the current unreal-mcp.exe plus the newest prev-*.exe (the
  one-step rollback copy), and deletes logs and backup leftovers.
.PARAMETER WhatIf
  Show what would be removed without deleting.
#>
[CmdletBinding(SupportsShouldProcess)]
param()
$ErrorActionPreference = 'Stop'
$dist = Join-Path (Split-Path -Parent $PSScriptRoot) 'dist'
if (-not (Test-Path $dist)) { Write-Host '[clean] no dist/'; return }

$keep = @('unreal-mcp.exe')
$prev = Get-ChildItem $dist -Filter 'prev-*.exe' | Sort-Object LastWriteTime -Descending | Select-Object -First 1
if ($prev) { $keep += $prev.Name }

Get-ChildItem $dist -File | Where-Object { $keep -notcontains $_.Name } | ForEach-Object {
  if ($PSCmdlet.ShouldProcess($_.FullName, 'delete')) {
    Remove-Item $_.FullName -Force
    Write-Host "[clean] removed $($_.Name)"
  }
}
Write-Host "[clean] kept: $($keep -join ', ')"
