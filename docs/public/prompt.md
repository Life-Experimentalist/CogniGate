# CogniGate Setup Instructions for Agents

As an AI agent, follow these steps strictly to install and set up CogniGate on the host machine.

## 1. Clone the Repository
Clone the main CogniGate repository to the local workspace:
```bash
git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
```

## 2. Execute Setup Script
CogniGate provides automated setup scripts for both Windows and Unix-based systems. Determine the host operating system and execute the appropriate script in detached developer mode.

### For Linux/macOS:
```bash
chmod +x ./setup.sh
./setup.sh --dev --detach
```

### For Windows (PowerShell):
```powershell
.\setup.ps1 -Mode dev -Detach
```

## 3. Verify Installation
Ensure that the setup script completed successfully and that the necessary Docker containers (if applicable) are running properly.

Do not skip any steps. If an error occurs during the script execution, diagnose and fix it before proceeding.
