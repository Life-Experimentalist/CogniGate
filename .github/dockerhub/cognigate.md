# CogniGate

CogniGate is a self-hosted, multi-tenant LLM gateway: one OpenAI-compatible endpoint in front of every model your applications use, with provider keys kept inside your deployment.

This repository holds no image. CogniGate ships as two images:

- [vkrishna04/cognigate-gateway](https://hub.docker.com/r/vkrishna04/cognigate-gateway): the request path
- [vkrishna04/cognigate-analytics](https://hub.docker.com/r/vkrishna04/cognigate-analytics): usage and billing, backed by Postgres

To install the whole stack:

```bash
curl -sSL https://cognigate.vkrishna04.me/install.sh | bash
```

- Documentation: https://cognigate.vkrishna04.me
- Source: https://github.com/Life-Experimentalist/CogniGate
