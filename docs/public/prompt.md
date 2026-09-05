# CogniGate — Agent Bootstrap

You are being asked to install and configure **CogniGate**, a self-hosted,
multi-tenant, OpenAI-compatible LLM gateway. Follow this document exactly.

The complete operating manual is at
<https://cognigate.vkrishna04.me/skill/SKILL.md>. If you can
fetch it, fetch it and follow it instead of this page — it covers provisioning,
routing, quotas, integration and troubleshooting, and this page does not.

If you are a Claude Code agent, install it as a skill once and it is available
in every future session:

```bash
mkdir -p ~/.claude/skills/cognigate && curl -fsSL https://cognigate.vkrishna04.me/skill/SKILL.md -o ~/.claude/skills/cognigate/SKILL.md
```

---

## If you cannot fetch anything, this is the minimum

**Prerequisites.** Docker and Docker Compose v2. Nothing else — no Go
toolchain, no JDK. Verify with `docker compose version` before continuing; if
it is missing, stop and say so rather than installing a toolchain the project
does not need.

**1. Clone and start.**

```bash
git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
./setup.sh --dev --detach
```

On Windows PowerShell: `.\setup.ps1 -Mode dev -Detach`.

**2. Verify, and do not skip this.** The setup script can print success on a
path that did nothing. Confirm the state yourself:

```bash
docker compose ps
curl -fsS http://localhost:8080/healthz
```

Three containers must be up — `gateway` (8080), `analytics` (8081),
`postgres-db` (5432). There is no fourth.

**3. If a service exited**, the cause is almost always that `.env` still
contains `replace_me` for `GATEWAY_BOOTSTRAP_KEY` or `ANALYTICS_TOKEN`. Both
are refused at startup on purpose. Generate real values —
`openssl rand -hex 24` and `openssl rand -hex 32` — write them into `.env`, and
`docker compose up -d`. Never echo the resulting values into the transcript.

**4. Report what you did**, including the tenant id and the container states.
Do not report success on the strength of a script's own output.

## Rules

- Never print, log or commit a provider key or a minted `cg-` secret.
- The key-mint response shows the secret exactly once, at `.secret`. `.key` is
  metadata only.
- Never run `./setup.sh --clean` to fix a failing container. It deletes the
  Postgres volume, and with it every tenant, key, provider and usage record.
- If a step fails, diagnose it and say what was wrong. Do not work around it by
  pointing an application at a provider directly — that reintroduces exactly
  the credential exposure this gateway exists to remove.
