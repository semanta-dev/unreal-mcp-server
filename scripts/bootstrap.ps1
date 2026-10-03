<#
.SYNOPSIS
  Bootstraps a user-local Go toolchain for building unreal-mcp.exe. No admin required.

.DESCRIPTION
  P0 of GO_REWRITE_PLAN.md. Installs a pinned, checksum-verified Go SDK into
  %LOCALAPPDATA%\go-sdk\go<VER>, adds its bin to the *user* PATH (idempotent), and
  sets GOTOOLCHAIN=local so builds are deterministic. Safe to re-run.

  Pin is intentional (reproducible builds). To bump Go, update $Version/$Sha256
  from https://go.dev/dl/?mode=json (windows-amd64 archive).
#>
[CmdletBinding()]
param(
  [string]$Version = 'go1.26.4',
  [string]$Sha256  = '3ca8fb4630b07c419cbdd51f754e31363cfcfb83b3a5354d9e895c90be2cc345',
  [switch]$AddToUserPath = $true
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# go.mod is the single source of the Go version; the pinned archive hash below must match it.
$modVer = 'go' + ((Select-String -Path (Join-Path (Split-Path -Parent $PSScriptRoot) 'go.mod') -Pattern '^go\s+(\S+)').Matches[0].Groups[1].Value)
if ($modVer -ne $Version) { throw "go.mod pins $modVer but bootstrap pins $Version - bump `$Version and `$Sha256 together with go.mod." }

$sdkRoot   = Join-Path $env:LOCALAPPDATA 'go-sdk'
$goRoot    = Join-Path $sdkRoot $Version
$goBin     = Join-Path $goRoot 'bin'
$goExe     = Join-Path $goBin  'go.exe'

function Test-GoOk {
  param([string]$exe)
  if (-not (Test-Path $exe)) { return $false }
  try { $v = & $exe version 2>$null } catch { return $false }
  return ($v -match [regex]::Escape($Version))
}

if (Test-GoOk $goExe) {
  Write-Host "[bootstrap] $Version already installed at $goRoot"
} else {
  $url = "https://go.dev/dl/$Version.windows-amd64.zip"
  $tmp = Join-Path $env:TEMP "$Version.windows-amd64.zip"
  Write-Host "[bootstrap] Downloading $url"
  Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing

  Write-Host "[bootstrap] Verifying SHA256"
  $actual = (Get-FileHash -Path $tmp -Algorithm SHA256).Hash.ToLower()
  if ($actual -ne $Sha256.ToLower()) {
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue
    throw "SHA256 mismatch for $Version. expected=$Sha256 actual=$actual"
  }
  Write-Host "[bootstrap] Checksum OK"

  # Extract to a temp dir (zip has a top-level 'go/'), then move to a versioned root.
  $stage = Join-Path $sdkRoot ".stage-$Version"
  if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
  New-Item -ItemType Directory -Path $stage -Force | Out-Null
  Write-Host "[bootstrap] Extracting (this takes ~30s)"
  Expand-Archive -Path $tmp -DestinationPath $stage -Force

  if (Test-Path $goRoot) { Remove-Item $goRoot -Recurse -Force }
  Move-Item -Path (Join-Path $stage 'go') -Destination $goRoot
  Remove-Item $stage -Recurse -Force
  Remove-Item $tmp -Force -ErrorAction SilentlyContinue

  if (-not (Test-GoOk $goExe)) { throw "Go install verification failed at $goExe" }
  Write-Host "[bootstrap] Installed $Version to $goRoot"
}

# Deterministic toolchain: never auto-download a different Go for this project.
& $goExe env -w GOTOOLCHAIN=local | Out-Null

if ($AddToUserPath) {
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($userPath -split ';') -notcontains $goBin) {
    $newPath = if ([string]::IsNullOrEmpty($userPath)) { $goBin } else { "$userPath;$goBin" }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Write-Host "[bootstrap] Added $goBin to user PATH (open a new shell to pick it up)"
  } else {
    Write-Host "[bootstrap] $goBin already on user PATH"
  }
}

Write-Host ""
Write-Host "[bootstrap] go version:" (& $goExe version)
Write-Host "[bootstrap] GOROOT:" (& $goExe env GOROOT)
Write-Host "[bootstrap] GOPATH:" (& $goExe env GOPATH)
Write-Host "[bootstrap] go.exe -> $goExe"
Write-Host "[bootstrap] DONE"
