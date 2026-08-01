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
check_cmd docker-compose 2>/dev/null || check_cmd "docker compose"

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

# Validate critical env var
source .env 2>/dev/null || true
if [ -z "${ENCRYPTION_MASTER_KEY:-}" ] || [ "${ENCRYPTION_MASTER_KEY}" = "replace_with_32_byte_hex_key_here_minimum_64_chars" ]; then
  echo -e "${YELLOW}⚠ ENCRYPTION_MASTER_KEY is not set or is still the placeholder.${NC}"
  echo -e "${YELLOW}  Generating a secure key for you...${NC}"
  NEW_KEY=$(openssl rand -hex 32 2>/dev/null || python3 -c "import secrets; print(secrets.token_hex(32))")
  sed -i.bak "s/ENCRYPTION_MASTER_KEY=.*/ENCRYPTION_MASTER_KEY=${NEW_KEY}/" .env
  echo -e "${GREEN}✓ ENCRYPTION_MASTER_KEY generated and saved to .env${NC}"
fi

# --- Clean Volumes (optional) ---
if [ "$CLEAN" = true ]; then
  echo -e "${BLUE}[3/4] Removing old data volumes...${NC}"
  docker-compose down -v --remove-orphans 2>/dev/null || true
  echo -e "${GREEN}✓ Old volumes removed${NC}"
else
  echo -e "${BLUE}[3/4] Skipping volume cleanup (use --clean to wipe data)${NC}"
fi

# --- Build & Start ---
echo -e "${BLUE}[4/4] Building and starting containers...${NC}"
docker-compose up --build $DETACH

if [ -n "$DETACH" ]; then
  echo ""
  echo -e "${GREEN}${BOLD}✓ CogniGate is running!${NC}"
  echo ""
  echo -e "  ${BOLD}Edge Proxy:${NC}       http://localhost:8080"
  echo -e "  ${BOLD}Domain Engine:${NC}    http://localhost:8081"
  echo -e "  ${BOLD}PostgreSQL:${NC}       localhost:5432 (db: cognigate)"
  echo -e "  ${BOLD}Redis:${NC}            localhost:6379"
  echo ""
  echo -e "  Run ${YELLOW}docker-compose logs -f${NC} to tail all logs."
  echo -e "  Run ${YELLOW}docker-compose down${NC} to stop all services."
  echo ""
  echo -e "  ${BOLD}Quick test:${NC}"
  echo -e "  ${YELLOW}curl -i http://localhost:8080/v1/chat/completions \\"
  echo -e "    -H 'Authorization: Bearer test' \\"
  echo -e "    -H 'Content-Type: application/json' \\"
  echo -e "    -d '{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello CogniGate!\"}]}'${NC}"
fi
