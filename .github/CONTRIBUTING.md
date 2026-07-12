# Contributing to CogniGate

Thank you for your interest in contributing to CogniGate! This document provides guidelines and instructions to make the contribution process smooth for everyone.

## Code of Conduct

By participating in this project, you agree to abide by the [Code of Conduct](CODE_OF_CONDUCT.md). Please report unacceptable behavior to the maintainers.

---

## Getting Started

### 1. Fork & Clone

```bash
git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
```

### 2. Set Up Development Environment

**Prerequisites:**
- Docker & Docker Compose
- Java 25 (Eclipse Temurin recommended)
- Go 1.26
- Node.js 20+ (for the docs site)

**One-command local setup:**

```bash
# Linux / macOS
./setup.sh --dev

# Windows (PowerShell)
.\setup.ps1 -Mode dev
```

This will:
1. Copy `.env.example` → `.env` and auto-generate an encryption key
2. Build all Docker images
3. Start all services in development mode

---

## Development Workflow

### Project Structure

```
CogniGate/
├── gateway/          # Go 1.26 — Edge Proxy
├── analytics/        # Java 25 / Spring Boot 4.1 — Domain Engine
├── docs/             # Next.js 15 — GitHub Pages site
└── .github/          # CI/CD workflows & community files
```

### Making Changes

1. Create a feature branch from `main`:
   ```bash
   git checkout -b feat/your-feature-name
   ```

2. Make your changes following the [coding standards](#coding-standards) below.

3. Write or update tests:
   ```bash
   # Java tests
   cd analytics && mvn test

   # Go tests
   cd gateway && go test ./...
   ```

4. Commit with a conventional commit message:
   ```bash
   git commit -m "feat(gateway): add Redis TTL configuration per tenant"
   ```

5. Push and open a Pull Request — fill in the PR template.

---

## Coding Standards

### Go (`/gateway`)
- Follow [Effective Go](https://go.dev/doc/effective_go)
- Run `gofmt` before committing
- All exported functions must have a doc comment
- Use `context.Context` propagation for all Redis and HTTP calls

### Java (`/analytics`)
- Follow standard Java conventions (Oracle Code Conventions)
- Use Lombok (`@Data`, `@Builder`, etc.) for model classes
- Services must be stateless and thread-safe (tested with Virtual Threads)
- Use constructor injection, not `@Autowired` field injection
- All `@Service` and `@RestController` methods must have Javadoc

### Commits
Follow [Conventional Commits](https://www.conventionalcommits.org/):
```
feat(component): add feature
fix(component): resolve bug
docs: update README
test(component): add unit tests
refactor(component): simplify logic
ci: update GitHub Actions workflow
```

---

## Pull Request Guidelines

- Keep PRs focused on a single concern
- Include tests for any new functionality
- Update `.env.example` if you add new environment variables
- Update the relevant docs pages in `/docs`
- Do not merge your own PR — request a review

---

## Reporting Issues

- Use [GitHub Issues](https://github.com/Life-Experimentalist/CogniGate/issues/new/choose)
- Check existing issues before opening a new one
- For security vulnerabilities, see [SECURITY.md](SECURITY.md)

---

## Questions & Discussion

- Use [GitHub Discussions](https://github.com/Life-Experimentalist/CogniGate/discussions) for general questions
- For quick questions, open a Discussion rather than an Issue

Thank you for contributing to CogniGate!
