#!/usr/bin/env bash
# =============================================================================
# CogniGate — One-Command Setup Script (Linux / macOS)
# Copyright 2026 VKrishna04 and Life Experimentalist
# Licensed under Apache 2.0
#
# Usage:
#   ./setup.sh [OPTIONS]
#
# Options:
#   --dev       Start in development mode (no TLS, verbose logging)
#   --prod      Start in production mode
#   --detach    Run containers in background (default: foreground)
#   --clean     Remove existing data volumes before starting
#   --help      Show this help message
# =============================================================================

set -euo pipefail

# --- Colors ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

# --- Banner ---
echo -e "${BLUE}${BOLD}"
cat << 'EOF'
  ____                 _  ____       _
 / ___|___   __ _ _ __(_)/ ___| __ _| |_ ___
| |   / _ \ / _` | '__| | |  _ / _` | __/ _ \
| |__| (_) | (_| | |  | | |_| | (_| | ||  __/
 \____\___/ \__, |_|  |_|\____|\__,_|\__\___|
            |___/
  The Zero-Downtime Cognitive Router for Enterprise AI
  https://github.com/Life-Experimentalist/CogniGate
EOF
echo -e "${NC}"

# --- Parse Arguments ---
MODE="dev"
DETACH=""
CLEAN=false

for arg in "$@"; do
  case $arg in
    --dev)    MODE="dev" ;;
    --prod)   MODE="prod" ;;
    --detach) DETACH="-d" ;;
    --clean)  CLEAN=true ;;
    --help)
      echo "Usage: ./setup.sh [--dev|--prod] [--detach] [--clean]"
      exit 0
      ;;
  esac
done

echo -e "${BOLD}[CogniGate] Starting in ${YELLOW}${MODE}${NC}${BOLD} mode...${NC}"

# --- Prerequisite Checks ---
echo -e "${BLUE}[1/4] Checking prerequisites...${NC}"

check_cmd() {
  if ! command -v "$1" &>/dev/null; then
    echo -e "${RED}✗ '$1' not found. Please install it first.${NC}"
    exit 1
  fi
  echo -e "${GREEN}✓ $1${NC}"
}

check_cmd docker

# Compose v2 is a docker subcommand, not a binary on PATH, so `command -v`
# cannot find it. v1 — the hyphenated `docker-compose` — reached end of life and
# is absent from current installs, so probing for it first only ever produced a
# false negative on a machine that was perfectly capable of running the stack.
if ! docker compose version &>/dev/null; then
  echo -e "${RED}✗ Docker Compose v2 not found. It ships with Docker Desktop, and as the docker-compose-plugin package on Linux.${NC}"
  exit 1
fi
echo -e "${GREEN}✓ docker compose${NC}"

# --- Environment Setup ---
echo -e "${BLUE}[2/4] Setting up environment...${NC}"

if [ ! -f ".env" ]; then
  if [ -f ".env.example" ]; then
    cp .env.example .env
    echo -e "${YELLOW}⚠ Created .env from .env.example. Please review and update secrets before production use.${NC}"
  else
    echo -e "${RED}✗ .env.example not found. Cannot create .env.${NC}"
    exit 1
  fi
else
  echo -e "${GREEN}✓ .env file exists${NC}"
fi

# Both credentials in .env.example are deliberately unusable: the analytics
# engine refuses a master key that is not 32 bytes of hex, and the gateway
# refuses a bootstrap key shorter than 16 characters. That makes an unedited
# copy fail loudly at startup rather than becoming a deployment whose secrets
# are published in this repository — so this is where it gets edited.
# shellcheck disable=SC1091
source .env 2>/dev/null || true

randhex() {
  openssl rand -hex "$1" 2>/dev/null \
    || python3 -c "import secrets,sys; print(secrets.token_hex(int(sys.argv[1])))" "$1"
}

# `|` as the delimiter because a hex secret can contain `/`. The .bak file is
# removed rather than left behind: it is a copy of the credentials, and BSD sed
# requires the suffix, so it cannot simply be omitted.
set_env() {
  sed -i.bak "s|^$1=.*|$1=$2|" .env && rm -f .env.bak
}

if [ -z "${ENCRYPTION_MASTER_KEY:-}" ] || [ "${ENCRYPTION_MASTER_KEY}" = "replace_with_32_byte_hex_key_here_minimum_64_chars" ]; then
  echo -e "${YELLOW}⚠ ENCRYPTION_MASTER_KEY is not set or is still the placeholder. Generating one...${NC}"
  set_env ENCRYPTION_MASTER_KEY "$(randhex 32)"
  echo -e "${GREEN}✓ ENCRYPTION_MASTER_KEY generated and saved to .env${NC}"
fi

if [ -z "${GATEWAY_BOOTSTRAP_KEY:-}" ] || [ "${GATEWAY_BOOTSTRAP_KEY}" = "replace_me" ]; then
  echo -e "${YELLOW}⚠ GATEWAY_BOOTSTRAP_KEY is not set or is still the placeholder. Generating one...${NC}"
  set_env GATEWAY_BOOTSTRAP_KEY "$(randhex 24)"
  echo -e "${GREEN}✓ GATEWAY_BOOTSTRAP_KEY generated and saved to .env${NC}"
fi

# --- Clean Volumes (optional) ---
if [ "$CLEAN" = true ]; then
  echo -e "${BLUE}[3/4] Removing old data volumes...${NC}"
  docker compose down -v --remove-orphans 2>/dev/null || true
  echo -e "${GREEN}✓ Old volumes removed${NC}"
else
  echo -e "${BLUE}[3/4] Skipping volume cleanup (use --clean to wipe data)${NC}"
fi

# --- Build & Start ---
echo -e "${BLUE}[4/4] Building and starting containers...${NC}"
if [ -n "$DETACH" ]; then
  # --wait blocks until every service reports healthy, so the summary below
  # describes a stack that is actually serving rather than one that has merely
  # been created. The timeout is generous because a cold JVM is slow to boot.
  docker compose up --build -d --wait --wait-timeout 180
else
  docker compose up --build
fi

if [ -n "$DETACH" ]; then
  echo ""
  echo -e "${GREEN}${BOLD}✓ CogniGate is running!${NC}"
  echo ""
  echo -e "  ${BOLD}Gateway:${NC}          http://localhost:8080"
  echo -e "  ${BOLD}Analytics:${NC}        http://localhost:8081"
  echo -e "  ${BOLD}PostgreSQL:${NC}       localhost:5432 (db: cognigate)"
  echo -e "  ${BOLD}Redis:${NC}            localhost:6379"
  echo ""
  echo -e "  Run ${YELLOW}docker compose logs -f${NC} to tail all logs."
  echo -e "  Run ${YELLOW}docker compose down${NC} to stop all services."
  echo ""
  # No completion is offered as a quick test: there is no provider configured
  # yet, so one could only fail. These two do work, and the second is the step
  # that gets the deployment its first usable credential.
  echo -e "  ${BOLD}Quick test:${NC}"
  echo -e "  ${YELLOW}curl -s http://localhost:8080/healthz${NC}"
  echo ""
  echo -e "  ${BOLD}Create your first tenant${NC} (the bootstrap key in .env is the only"
  echo -e "  credential that exists before a tenant does):"
  echo -e "  ${YELLOW}curl -s -X POST http://localhost:8080/admin/v1/tenants \\"
  echo -e "    -H \"Authorization: Bearer \$(grep '^GATEWAY_BOOTSTRAP_KEY=' .env | cut -d= -f2-)\" \\"
  echo -e "    -H 'Content-Type: application/json' -d '{\"name\":\"my-org\"}'${NC}"
  echo ""
  echo -e "  Then mint a data-plane key against the returned tenant id — see the"
  echo -e "  ${BOLD}Quick Start${NC} section of README.md."
fi
