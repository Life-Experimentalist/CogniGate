# Security Policy

## Supported Versions

| Version         | Supported             |
| --------------- | --------------------- |
| `main` (latest) | ✅ Active              |
| `< 0.1.0`       | ❌ No longer supported |

## Reporting a Vulnerability

**Please do NOT report security vulnerabilities through public GitHub issues.**

### Preferred Method: GitHub Security Advisories

1. Navigate to [Security Advisories](https://github.com/Life-Experimentalist/CogniGate/security/advisories/new)
2. Click **"New draft security advisory"**
3. Fill in the details including:
   - A description of the vulnerability
   - Steps to reproduce
   - Potential impact assessment
   - Any suggested fixes (optional)

### Response Timeline

| Action              | Target Timeframe               |
| ------------------- | ------------------------------ |
| Acknowledge receipt | Within 48 hours                |
| Confirm or decline  | Within 7 days                  |
| Patch development   | Within 30 days of confirmation |
| Public disclosure   | After patch is released        |

---

## Scope

The following areas are in scope for security reports:

- **Authentication bypass** in the Gateway or Admin API
- **Injection attacks** (SQL, command, header injection)
- **Cryptographic weaknesses** in `EncryptionService` (AES-256-GCM implementation)
- **ClassLoader escapes** or arbitrary code execution through the Janino Plugin Engine
- **Privilege escalation** between tenants (multi-tenancy isolation)
- **Exposure of decrypted API keys** in logs or Redis

The following are out of scope:
- Denial of Service (DoS) attacks against rate-limited endpoints
- Issues in third-party dependencies that have already been publicly disclosed
- Theoretical vulnerabilities without a working proof of concept

---

## Encryption Key Management Notice

CogniGate uses an **AES-256-GCM master key** (`ENCRYPTION_MASTER_KEY`) to encrypt all third-party LLM provider API keys at rest. This key is the most sensitive secret in the deployment. **Always:**

1. Generate it with `openssl rand -hex 32` — never use the example placeholder
2. Store it exclusively in the `.env` file or a secrets manager (Vault, AWS Secrets Manager, etc.)
3. Never commit it to version control
4. Rotate it if you suspect it has been compromised

---

## Responsible Disclosure

We follow coordinated disclosure. We ask that:
- You give us reasonable time to fix the issue before public disclosure
- You do not exploit the vulnerability in production systems
- You do not share the vulnerability with anyone else until it is patched

We credit all security researchers who report valid vulnerabilities in our release notes.
