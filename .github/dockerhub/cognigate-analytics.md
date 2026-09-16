# CogniGate analytics

The usage and billing service of [CogniGate](https://github.com/Life-Experimentalist/CogniGate), a self-hosted, multi-tenant LLM gateway. It receives usage records from [cognigate-gateway](https://hub.docker.com/r/vkrishna04/cognigate-gateway), stores them in Postgres, and serves the usage and cost reports.

## Tags

- `latest`: the newest build of `main`
- `X.Y.Z` and `X.Y`: releases
- Images are published for `linux/amd64` and `linux/arm64`, with build provenance attestations. The same images are on `ghcr.io/life-experimentalist/cognigate-analytics`.

## Run it

This image is meant to run next to the gateway and a Postgres database. The simplest way to get all three is:

```bash
curl -sSL https://cognigate.vkrishna04.me/install.sh | bash
```

To run it yourself, give it a database and the same token the gateway uses:

| Setting | Purpose |
| --- | --- |
| port `8081` | ingest API for the gateway, reports, `/actuator/health` |
| `SPRING_DATASOURCE_URL` | JDBC URL, e.g. `jdbc:postgresql://db:5432/cognigate` |
| `SPRING_DATASOURCE_USERNAME`, `SPRING_DATASOURCE_PASSWORD` | database credentials |
| `ANALYTICS_TOKEN` | shared secret; must match the gateway's (generate with `openssl rand -hex 32`) |

Port 8081 accepts usage writes, so do not expose it beyond the network the gateway runs on.

## Links

- Documentation: https://cognigate.vkrishna04.me
- Source and issues: https://github.com/Life-Experimentalist/CogniGate
- License: Apache 2.0
