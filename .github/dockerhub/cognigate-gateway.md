# CogniGate gateway

The request path of [CogniGate](https://github.com/Life-Experimentalist/CogniGate), a self-hosted, multi-tenant LLM gateway. Applications hold one CogniGate key and speak the OpenAI API; provider credentials stay inside the deployment.

It handles authentication, capability aliases (`fast`, `balanced`, `best`), fallback chains, circuit breakers, rate limits and quotas, and optional response caching. Usage is sent to the companion [cognigate-analytics](https://hub.docker.com/r/vkrishna04/cognigate-analytics) image for durable storage.

## Tags

- `latest`: the newest build of `main`
- `X.Y.Z` and `X.Y`: releases
- Images are published for `linux/amd64` and `linux/arm64`, with build provenance attestations. The same images are on `ghcr.io/life-experimentalist/cognigate-gateway`.

## Run it

The supported way to run CogniGate is the full stack (gateway, analytics, Postgres):

```bash
curl -sSL https://cognigate.vkrishna04.me/install.sh | bash
```

Read the script first if you prefer; it clones the repository, generates credentials and starts `docker compose`.

The gateway on its own:

```bash
docker run -p 8080:8080 \
  -e CG_ADMIN_BOOTSTRAP_KEY=<generate with: openssl rand -hex 24> \
  vkrishna04/cognigate-gateway:latest
```

Without `ANALYTICS_URL` and `ANALYTICS_TOKEN` the gateway keeps usage in memory, and a restart loses it.

| Setting | Purpose |
| --- | --- |
| port `8080` | OpenAI-compatible API, admin API, `/healthz` |
| `CG_ADMIN_BOOTSTRAP_KEY` | first admin credential, used to create tenants and keys |
| `ANALYTICS_URL`, `ANALYTICS_TOKEN` | where usage is written |
| `CG_QUOTA_ENFORCEMENT` | `on` (default) or `observe` |
| `CG_CACHE_ENABLED` | `true` to turn on response caching |
| `-config <path>` | optional configuration file; mount it and pass the flag |

## Links

- Documentation: https://cognigate.vkrishna04.me
- Source and issues: https://github.com/Life-Experimentalist/CogniGate
- License: Apache 2.0
