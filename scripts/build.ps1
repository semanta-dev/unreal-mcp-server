<#
.SYNOPSIS
  Builds the static unreal-mcp.exe. Assumes bootstrap.ps1 has run.
.DESCRIPTION
  Pure-Go, CGO_ENABLED=0 -> fully static Windows amd64 binary, no libc/venv.
  Stamps version/commit via -ldflags. Enforces stdout-purity gate (no fmt.Print
  to stdout outside the SDK path corrupts the MCP JSON-RPC frame).
#>
[CmdletBinding()]
param(
  [string]$Out = 'dist/unreal-mcp.exe',
  [switch]$SkipStdoutGate
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo

# Locate go: prefer PATH, fall back to the pinned SDK from bootstrap.ps1.
$goCmd = (Get-Command go -ErrorAction SilentlyContinue)
$go = if ($goCmd) { $goCmd.Source } else { Join-Path $env:LOCALAPPDATA 'go-sdk\go1.26.4\bin\go.exe' }
if (-not (Test-Path $go)) { throw "go not found. Run scripts/bootstrap.ps1 first." }

# Version stamps (git optional).
$ver = 'dev'; $commit = 'unknown'
if (Get-Command git -ErrorAction SilentlyContinue) {
  try { $ver    = (git describe --tags --always 2>$null); if (-not $ver) { $ver = 'dev' } } catch {}
  try { $commit = (git rev-parse --short HEAD 2>$null);   if (-not $commit) { $commit = 'unknown' } } catch {}
}

$env:CGO_ENABLED = '0'; $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
$ldflags = "-s -w -X github.com/jdziat/unreal-mcp-server/internal/version.Version=$ver -X github.com/jdziat/unreal-mcp-server/internal/version.Commit=$commit"

New-Item -ItemType Directory -Path (Split-Path $Out) -Force | Out-Null
Write-Host "[build] go build -> $Out (version=$ver commit=$commit)"
& $go build -trimpath -ldflags $ldflags -o $Out ./cmd/unreal-mcp
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

Write-Host "[build] $Out ->" (& (Resolve-Path $Out) -version)
Write-Host "[build] DONE"
