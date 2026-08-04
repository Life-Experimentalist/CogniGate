## Description

<!-- Provide a concise summary of the change and which issue it addresses. -->

Closes #<!-- Issue number -->

## Type of Change

<!-- Check all that apply -->
- [ ] 🐛 Bug fix (non-breaking change that fixes an issue)
- [ ] ✨ New feature (non-breaking change that adds functionality)
- [ ] 💥 Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] 📚 Documentation update
- [ ] 🏗️ Refactor / code cleanup
- [ ] 🧪 Tests only

## Component(s) Affected

- [ ] `gateway/` (Go Edge Proxy)
- [ ] `analytics/` (Spring Boot Domain Engine)
- [ ] `docker-compose.yml` / infrastructure
- [ ] `.github/` (CI/CD, community files)
- [ ] `docs/` (documentation site)

## Testing

<!-- Describe how you tested your changes. -->

- [ ] Unit tests pass: `cd analytics && mvn test` / `cd gateway && go test ./...`
- [ ] Docker Compose build succeeds: `docker-compose up --build -d`
- [ ] Manual smoke test performed (describe below)

**Manual test steps:**
<!-- e.g., "Ran curl to POST /v1/chat/completions and received 200" -->

## Checklist

- [ ] My code follows the project's style guidelines
- [ ] I have added/updated tests that prove my fix/feature works
- [ ] I have updated relevant documentation
- [ ] I have added a changelog entry (if applicable)
- [ ] I have not introduced any new environment variables without updating `.env.example`
