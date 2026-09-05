#!/usr/bin/env bash
# =============================================================================
# CogniGate — Interactive Setup Wrapper (Linux / macOS)
# Copyright 2026 VKrishna04 and Life Experimentalist
#
# Execution: curl -sSL https://cognigate.vkrishna04.me/install.sh | bash
# =============================================================================

set -e

# --- Colors ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# This script is documented as `curl -sSL ... | bash`, which makes stdin the
# script itself — a bare `read` would consume the script's own remaining
# bytes rather than wait for the user. Prompt against the terminal instead,
# and fall back to the defaults when there is no terminal to ask.
# Testing for the device file is not enough: /dev/tty exists even in a
# session with no controlling terminal, and only opening it fails. Open it
# once, on descriptor 3, and let that decide.
if { exec 3<>/dev/tty; } 2>/dev/null; then
  HAVE_TTY=1
else
  HAVE_TTY=0
fi

# ask <variable> <prompt> — with no terminal, leaves the variable empty so
# each call site falls through to its own documented default.
ask() {
  if [ "$HAVE_TTY" = 1 ]; then
    printf '%b' "$2" >&3
    IFS= read -r "$1" <&3
  else
    eval "$1=''"
  fi
}

echo -e "${BLUE}${BOLD}"
cat << 'EOF'
  ____                 _  ____       _
 / ___|___   __ _ _ __(_)/ ___| __ _| |_ ___
| |   / _ \ / _` | '__| | |  _ / _` | __/ _ \
| |__| (_) | (_| | |  | | |_| | (_| | ||  __/
 \____\___/ \__, |_|  |_|\____|\__,_|\__\___|
            |___/
EOF
echo -e "${NC}"
echo -e "${GREEN}${BOLD}Welcome to the CogniGate Interactive Installer!${NC}\n"

# --- System Checks ---
echo -e "${BLUE}Performing system checks...${NC}"

check_cmd() {
  if ! command -v "$1" &>/dev/null; then
    echo -e "${RED}✗ '$1' is not installed or not in PATH.${NC}"
    echo -e "${YELLOW}  Please install $1 before continuing.${NC}"
    exit 1
  fi
  echo -e "${GREEN}✓ $1 found${NC}"
}

check_cmd git
check_cmd docker
echo ""

# --- Installation Directory ---
CURRENT_DIR=$(pwd)
echo -e "Current Directory: ${CYAN}${CURRENT_DIR}${NC}"
ask choice "Do you want to install CogniGate in the current directory? (y/N) "

if [[ "$choice" =~ ^[Yy]$ ]]; then
  INSTALL_DIR="$CURRENT_DIR"
else
  ask folder_name "Enter the name of the new folder to create (e.g. cognigate) [CogniGate]: "
  folder_name=${folder_name:-CogniGate}
  INSTALL_DIR="$CURRENT_DIR/$folder_name"
  mkdir -p "$INSTALL_DIR"
fi

echo -e "\nInstalling to: ${CYAN}${INSTALL_DIR}${NC}"
cd "$INSTALL_DIR"

# --- Clone Repository ---
if [ ! -d ".git" ]; then
  echo -e "${BLUE}Cloning repository...${NC}"
  if ! git clone https://github.com/Life-Experimentalist/CogniGate.git .; then
    echo -e "${RED}Failed to clone repository.${NC}"
    exit 1
  fi
else
  echo -e "${BLUE}Git repository already exists. Pulling latest...${NC}"
  git pull
fi

echo -e "\n${GREEN}Repository ready.${NC}"

# --- Setup Execution ---
ask run_setup "Do you want to start the CogniGate cluster now (Production Mode, Detached)? (Y/n) "
if [[ ! "$run_setup" =~ ^[Nn]$ ]]; then
  echo -e "${BLUE}Starting setup...${NC}"
  if [ -f "setup.sh" ]; then
    chmod +x setup.sh
    ./setup.sh --prod --detach
  else
    echo -e "${RED}setup.sh not found in the repository root!${NC}"
    exit 1
  fi
else
  echo -e "${YELLOW}Setup skipped. You can manually start the cluster later by running: ./setup.sh --prod --detach${NC}"
fi

echo -e "\n${GREEN}${BOLD}Installation Complete!${NC}"
