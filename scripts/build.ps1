<#
.SYNOPSIS
  Gates and builds the static unreal-mcp.exe. Assumes bootstrap.ps1 has run.
.DESCRIPTION
  Runs the same gates as CI before building: gofmt, go vet, the stdout-purity
  gate (stdout carries the MCP JSON-RPC frame — no fmt.Print/os.Stdout in
  internal/), and go test. Then builds a pure-Go, CGO_ENABLED=0 static Windows
  amd64 binary stamped with version/commit via -ldflags.
.PARAMETER Fast
  Skip go test (gofmt/vet/stdout gates still run).
#>
[CmdletBinding()]
param(
  [string]$Out = 'dist/unreal-mcp.exe',
  [switch]$Fast
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo

# Go version is single-sourced from go.mod.
$goVer = 'go' + ((Select-String -Path go.mod -Pattern '^go\s+(\S+)').Matches[0].Groups[1].Value)

# Locate go: prefer PATH, fall back to the pinned SDK from bootstrap.ps1.
$goCmd = (Get-Command go -ErrorAction SilentlyContinue)
$go = if ($goCmd) { $goCmd.Source } else { Join-Path $env:LOCALAPPDATA "go-sdk\$goVer\bin\go.exe" }
if (-not (Test-Path $go)) { throw "go not found. Run scripts/bootstrap.ps1 first." }
$gofmt = Join-Path (Split-Path $go) 'gofmt.exe'
$env:GOTOOLCHAIN = 'local'

function Invoke-Gate([string]$name, [scriptblock]$body) {
  Write-Host "[gate] $name"
  & $body
  if ($LASTEXITCODE -ne 0) { throw "gate failed: $name" }
}

Invoke-Gate 'gofmt' {
  $bad = & $gofmt -l internal cmd
  if ($bad) { Write-Host $bad; $global:LASTEXITCODE = 1 } else { $global:LASTEXITCODE = 0 }
}
Invoke-Gate 'go vet' { & $go vet ./... }
Invoke-Gate 'stdout purity' {
  $hits = Get-ChildItem -Recurse -Path internal -Filter *.go |
    Where-Object { $_.Name -notlike '*_test.go' } |
    Select-String -Pattern 'fmt\.Print(ln|f)?\(|os\.Stdout'
  if ($hits) { $hits | ForEach-Object { Write-Host $_ }; $global:LASTEXITCODE = 1 } else { $global:LASTEXITCODE = 0 }
}
if (-not $Fast) { Invoke-Gate 'go test' { & $go test ./... } }

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
