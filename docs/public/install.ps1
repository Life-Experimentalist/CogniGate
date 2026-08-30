# =============================================================================
# CogniGate - Interactive Setup Wrapper (Windows PowerShell)
# Copyright 2026 VKrishna04 and Life Experimentalist
#
# Execution: irm https://life-experimentalist.github.io/CogniGate/install.ps1 | iex
#
# This file must stay pure ASCII, for the same reason setup.ps1 must. Windows
# PowerShell 5.1 reads a .ps1 with no byte-order mark as ANSI, and .editorconfig
# requires utf-8 without one, so a multi-byte character here decodes into
# mojibake. A check mark is the worst case: U+2713 is E2 9C 93, and 0x93 is a
# left double quotation mark in cp1252, which opens a string that never closes
# and takes the rest of the file down with it. Write [OK] / [!] / [X].
# =============================================================================

$ErrorActionPreference = "Stop"

Write-Host @"
  ____                 _  ____       _
 / ___|___   __ _ _ __(_)/ ___| __ _| |_ ___
| |   / _ \  / _`` | '__| | |  _ / _`` | __/ _ \
| |__| (_) || (_| | |  | | |_| | (_| | ||  __/
 \____\___/  \__, |_|  |_|\____|\__,_|\__\___|
             |___/
"@ -ForegroundColor Cyan
Write-Host "Welcome to the CogniGate Interactive Installer!" -ForegroundColor Green
Write-Host ""

# --- System Checks ---
Write-Host "Performing system checks..." -ForegroundColor Blue

function Check-Command($cmd) {
    if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
        Write-Host "  [X] '$cmd' is not installed or not in PATH." -ForegroundColor Red
        Write-Host "    Please install $cmd before continuing." -ForegroundColor Yellow
        exit 1
    }
    Write-Host "  [OK] $cmd found" -ForegroundColor Green
}

Check-Command "git"
Check-Command "docker"

Write-Host ""
# --- Installation Directory ---
$currentDir = Get-Location
Write-Host "Current Directory: $currentDir" -ForegroundColor Cyan
$choice = Read-Host "Do you want to install CogniGate in the current directory? (y/N)"

if ($choice -match "^[yY]") {
    $installDir = $currentDir
} else {
    $folderName = Read-Host "Enter the name of the new folder to create (e.g. cognigate)"
    if ([string]::IsNullOrWhiteSpace($folderName)) {
        $folderName = "CogniGate"
    }
    $installDir = Join-Path $currentDir $folderName
    if (-not (Test-Path $installDir)) {
        New-Item -ItemType Directory -Path $installDir | Out-Null
    }
}

Write-Host ""
Write-Host "Installing to: $installDir" -ForegroundColor Cyan
Set-Location $installDir

# --- Clone Repository ---
if (-not (Test-Path ".git")) {
    Write-Host "Cloning repository..." -ForegroundColor Blue
    git clone https://github.com/Life-Experimentalist/CogniGate.git .
    if ($LASTEXITCODE -ne 0) {
        Write-Host "Failed to clone repository." -ForegroundColor Red
        exit 1
    }
} else {
    Write-Host "Git repository already exists. Pulling latest..." -ForegroundColor Blue
    git pull
}

Write-Host ""
Write-Host "Repository ready." -ForegroundColor Green

# --- Setup Execution ---
$runSetup = Read-Host "Do you want to start the CogniGate cluster now (Production Mode, Detached)? (Y/n)"
if ($runSetup -notmatch "^[nN]") {
    Write-Host "Starting setup..." -ForegroundColor Blue
    if (Test-Path "setup.ps1") {
        .\setup.ps1 -Mode prod -Detach
    } else {
        Write-Host "setup.ps1 not found in the repository root!" -ForegroundColor Red
        exit 1
    }
} else {
    Write-Host "Setup skipped. You can manually start the cluster later by running: .\setup.ps1 -Mode prod -Detach" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "Installation Complete!" -ForegroundColor Green
