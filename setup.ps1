# =============================================================================
# CogniGate - One-Command Setup Script (Windows PowerShell)
# Copyright 2026 VKrishna04 and Life Experimentalist
# Licensed under Apache 2.0
#
# Usage:
#   .\setup.ps1 [-Mode dev|prod] [-Detach] [-Clean] [-Help]
#
# This file must stay pure ASCII. Windows PowerShell 5.1 reads a .ps1 with no
# byte-order mark as ANSI, and .editorconfig requires utf-8 without one, so a
# multi-byte character here decodes into mojibake that can contain a stray
# quote and break parsing several lines later. Write [OK] / [!] / [X], not
# check marks.
# =============================================================================

param(
    [ValidateSet("dev","prod")]
    [string]$Mode = "dev",
    [switch]$Detach,
    [switch]$Clean,
    [switch]$Help
)

$ErrorActionPreference = "Stop"

# --- Banner ---
Write-Host @"
  ____                 _  ____       _
 / ___|___   __ _ _ __(_)/ ___| __ _| |_ ___
| |   / _ \  / _`` | '__| | |  _ / _`` | __/ _ \
| |__| (_) || (_| | |  | | |_| | (_| | ||  __/
 \____\___/  \__, |_|  |_|\____|\__,_|\__\___|
             |___/
  The Zero-Downtime Cognitive Router for Enterprise AI
  https://github.com/Life-Experimentalist/CogniGate
"@ -ForegroundColor Cyan

if ($Help) {
    Write-Host "Usage: .\setup.ps1 [-Mode dev|prod] [-Detach] [-Clean]"
    exit 0
}

Write-Host "[CogniGate] Starting in $Mode mode..." -ForegroundColor Yellow

# --- Prerequisite Checks ---
Write-Host "[1/4] Checking prerequisites..." -ForegroundColor Blue

function Check-Command($cmd) {
    if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
        Write-Host "  [X] '$cmd' not found. Please install it first." -ForegroundColor Red
        exit 1
    }
    Write-Host "  [OK] $cmd" -ForegroundColor Green
}

Check-Command "docker"

# Compose v2 is a docker subcommand rather than a binary on PATH, so
# Get-Command cannot see it. v1 - the hyphenated `docker-compose` - reached end
# of life and is absent from current installs.
docker compose version *> $null
if ($LASTEXITCODE -ne 0) {
    Write-Host "  [X] Docker Compose v2 not found. It ships with Docker Desktop." -ForegroundColor Red
    exit 1
}
Write-Host "  [OK] docker compose" -ForegroundColor Green

# --- Environment Setup ---
Write-Host "[2/4] Setting up environment..." -ForegroundColor Blue

if (-not (Test-Path ".env")) {
    if (Test-Path ".env.example") {
        Copy-Item ".env.example" ".env"
        Write-Host "  [!] Created .env from .env.example. Review secrets before production." -ForegroundColor Yellow
    } else {
        Write-Host "  [X] .env.example not found." -ForegroundColor Red
        exit 1
    }
} else {
    Write-Host "  [OK] .env file exists" -ForegroundColor Green
}

# Both credentials in .env.example are placeholders, and both are replaced
# here. Leaving either as shipped would mean a deployment whose secrets are
# published in this repository: the bootstrap key opens the admin plane, and
# the analytics token is the only thing in front of every tenant's usage on
# port 8081. The gateway refuses a bootstrap key shorter than 16 characters,
# so that one at least fails loudly; the token would simply work.
function New-HexSecret([int]$Bytes) {
    $buf = New-Object byte[] $Bytes
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($buf)
    return ($buf | ForEach-Object { $_.ToString("x2") }) -join ""
}

# Set-EnvValue, not a bare -replace. A substitution whose pattern matches
# nothing succeeds silently, so on a .env with no line for the key at all --
# one written by hand, or trimmed -- the old version reported that the secret
# had been generated and saved while writing nothing, and the stack then
# started with the variable unset.
function Set-EnvValue([string]$Content, [string]$Key, [string]$Value) {
    if ($Content -match "(?m)^$Key=") {
        return ($Content -replace "(?m)^$Key=.*", "$Key=$Value")
    }
    if ($Content.Length -gt 0 -and -not $Content.EndsWith("`n")) { $Content += "`n" }
    return $Content + "$Key=$Value`n"
}

$envContent = Get-Content ".env" -Raw
if ($envContent -match "(?m)^GATEWAY_BOOTSTRAP_KEY=replace_" -or $envContent -notmatch "(?m)^GATEWAY_BOOTSTRAP_KEY=.") {
    Write-Host "  [!] Generating GATEWAY_BOOTSTRAP_KEY..." -ForegroundColor Yellow
    $envContent = Set-EnvValue $envContent "GATEWAY_BOOTSTRAP_KEY" (New-HexSecret 24)
    Write-Host "  [OK] GATEWAY_BOOTSTRAP_KEY generated." -ForegroundColor Green
}

if ($envContent -match "(?m)^ANALYTICS_TOKEN=replace_" -or $envContent -notmatch "(?m)^ANALYTICS_TOKEN=.") {
    Write-Host "  [!] Generating ANALYTICS_TOKEN..." -ForegroundColor Yellow
    $envContent = Set-EnvValue $envContent "ANALYTICS_TOKEN" (New-HexSecret 32)
    Write-Host "  [OK] ANALYTICS_TOKEN generated." -ForegroundColor Green
}

# Written once, so a run that generates both keys does not leave a window in
# which .env holds one real credential and one placeholder. UTF8 without a BOM:
# docker compose reads .env byte-for-byte and a BOM would become part of the
# first variable's name.
[System.IO.File]::WriteAllText((Resolve-Path ".env"), $envContent, (New-Object System.Text.UTF8Encoding $false))

# --- Clean Volumes (optional) ---
if ($Clean) {
    Write-Host "[3/4] Removing old data volumes..." -ForegroundColor Blue
    docker compose down -v --remove-orphans 2>$null
    Write-Host "  [OK] Old volumes removed." -ForegroundColor Green
} else {
    Write-Host "[3/4] Skipping volume cleanup (use -Clean to wipe data)" -ForegroundColor Blue
}

# --- Build & Start ---
# -Mode dev compiles the images from this checkout. -Mode prod pulls the
# published multi-arch images, which is both faster and what install.ps1 asks
# for on a fresh clone. A pull that cannot complete - no network, a registry
# outage - falls back to building rather than leaving the caller with nothing.
$Build = "--build"
if ($Mode -eq "prod") {
    Write-Host "[4/4] Pulling published images..." -ForegroundColor Blue
    docker compose pull --quiet
    if ($LASTEXITCODE -eq 0) {
        $Build = ""
    } else {
        Write-Host "  [!] Could not pull the published images. Building from source instead." -ForegroundColor Yellow
    }
} else {
    Write-Host "[4/4] Building and starting containers..." -ForegroundColor Blue
}

if ($Detach) {
    # --wait blocks until every service reports healthy, so the summary below
    # describes a stack that is actually serving rather than one that has merely
    # been created. The timeout is generous because a cold JVM is slow to boot.
    if ($Build) {
        docker compose up --build -d --wait --wait-timeout 180
    } else {
        docker compose up -d --wait --wait-timeout 180
    }
} elseif ($Build) {
    docker compose up --build
} else {
    docker compose up
}

# $ErrorActionPreference does not apply to a native executable's exit code, so
# without this a stack that failed to come up still printed the success summary
# below. setup.sh gets the same guarantee from `set -e`.
if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "  [X] docker compose exited with $LASTEXITCODE. The stack is not running." -ForegroundColor Red
    Write-Host "      Run: docker compose logs" -ForegroundColor Yellow
    exit $LASTEXITCODE
}

if ($Detach) {
    Write-Host ""
    Write-Host "[OK] CogniGate is running!" -ForegroundColor Green
    Write-Host ""
    Write-Host "  Gateway:        http://localhost:8080"
    Write-Host "  Analytics:      http://localhost:8081"
    Write-Host "  PostgreSQL:     localhost:5432 (db: cognigate)"
    Write-Host ""
    Write-Host "  Run: docker compose logs -f  (to tail logs)"
    Write-Host "  Run: docker compose down     (to stop)"
    Write-Host ""
    # No completion is offered as a quick test: there is no provider configured
    # yet, so one could only fail. These two do work, and the second is the step
    # that gets the deployment its first usable credential.
    Write-Host "  Quick test:"
    Write-Host "  curl.exe -s http://localhost:8080/healthz" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  Create your first tenant (the bootstrap key in .env is the only"
    Write-Host "  credential that exists before a tenant does):"
    # Invoke-RestMethod rather than curl.exe. PowerShell rewrites the arguments
    # it hands to a native executable, and the inner double quotes of a JSON
    # body do not survive that rewrite - `-d '{"name":"my-org"}'` arrives as
    # {name:my-org} and the gateway rejects it as malformed. -Body takes the
    # string as given.
    Write-Host '  $b = (Select-String -Path .env -Pattern "^GATEWAY_BOOTSTRAP_KEY=").Line.Split("=",2)[1]' -ForegroundColor Yellow
    Write-Host '  Invoke-RestMethod -Method Post -Uri http://localhost:8080/admin/v1/tenants `' -ForegroundColor Yellow
    Write-Host '    -Headers @{ Authorization = "Bearer $b" } -ContentType "application/json" `' -ForegroundColor Yellow
    Write-Host '    -Body ''{"name":"my-org"}''' -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  Then mint a data-plane key against the returned tenant id - see the"
    Write-Host "  Quick Start section of README.md."
}
