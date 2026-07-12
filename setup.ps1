# =============================================================================
# CogniGate — One-Command Setup Script (Windows PowerShell)
# Copyright 2026 VKrishna04 and Life Experimentalist
# Licensed under Apache 2.0
#
# Usage:
#   .\setup.ps1 [-Mode dev|prod] [-Detach] [-Clean] [-Help]
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
        Write-Host "  ✗ '$cmd' not found. Please install it first." -ForegroundColor Red
        exit 1
    }
    Write-Host "  ✓ $cmd" -ForegroundColor Green
}

Check-Command "docker"

# --- Environment Setup ---
Write-Host "[2/4] Setting up environment..." -ForegroundColor Blue

if (-not (Test-Path ".env")) {
    if (Test-Path ".env.example") {
        Copy-Item ".env.example" ".env"
        Write-Host "  ⚠ Created .env from .env.example. Review secrets before production." -ForegroundColor Yellow
    } else {
        Write-Host "  ✗ .env.example not found." -ForegroundColor Red
        exit 1
    }
} else {
    Write-Host "  ✓ .env file exists" -ForegroundColor Green
}

# Check ENCRYPTION_MASTER_KEY
$envContent = Get-Content ".env" -Raw
if ($envContent -match "ENCRYPTION_MASTER_KEY=replace_" -or $envContent -notmatch "ENCRYPTION_MASTER_KEY=") {
    Write-Host "  ⚠ Generating ENCRYPTION_MASTER_KEY..." -ForegroundColor Yellow
    $bytes = New-Object byte[] 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    $newKey = ($bytes | ForEach-Object { $_.ToString("x2") }) -join ""
    $envContent = $envContent -replace "ENCRYPTION_MASTER_KEY=.*", "ENCRYPTION_MASTER_KEY=$newKey"
    Set-Content ".env" $envContent
    Write-Host "  ✓ ENCRYPTION_MASTER_KEY generated." -ForegroundColor Green
}

# --- Clean Volumes (optional) ---
if ($Clean) {
    Write-Host "[3/4] Removing old data volumes..." -ForegroundColor Blue
    docker-compose down -v --remove-orphans 2>$null
    Write-Host "  ✓ Old volumes removed." -ForegroundColor Green
} else {
    Write-Host "[3/4] Skipping volume cleanup (use -Clean to wipe data)" -ForegroundColor Blue
}

# --- Build & Start ---
Write-Host "[4/4] Building and starting containers..." -ForegroundColor Blue

$detachFlag = if ($Detach) { "-d" } else { "" }
if ($detachFlag) {
    docker-compose up --build -d
} else {
    docker-compose up --build
}

if ($Detach) {
    Write-Host ""
    Write-Host "✓ CogniGate is running!" -ForegroundColor Green
    Write-Host ""
    Write-Host "  Edge Proxy:     http://localhost:8080"
    Write-Host "  Domain Engine:  http://localhost:8081"
    Write-Host "  PostgreSQL:     localhost:5432 (db: cognigate)"
    Write-Host "  Redis:          localhost:6379"
    Write-Host ""
    Write-Host "  Run: docker-compose logs -f  (to tail logs)"
    Write-Host "  Run: docker-compose down      (to stop)"
    Write-Host ""
    Write-Host "  Quick test:"
    Write-Host "  curl -i http://localhost:8080/v1/chat/completions ``" -ForegroundColor Yellow
    Write-Host "    -H 'Authorization: Bearer test' ``" -ForegroundColor Yellow
    Write-Host "    -H 'Content-Type: application/json' ``" -ForegroundColor Yellow
    Write-Host "    -d '{`"model`":`"gpt-4`",`"messages`":[{`"role`":`"user`",`"content`":`"Hello!`"}]}'" -ForegroundColor Yellow
}
