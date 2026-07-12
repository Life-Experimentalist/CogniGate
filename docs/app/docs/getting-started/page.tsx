import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "Getting Started",
  description: "Get CogniGate running in minutes with one command. Prerequisites, quick start, and first AI call.",
};

export default function GettingStartedPage() {
  return (
    <article style={{ maxWidth: 780 }}>
      <h1 style={{ fontSize: 36, fontWeight: 800, color: "#f9fafb", marginBottom: 12, letterSpacing: "-1px" }}>
        Getting Started
      </h1>
      <p style={{ color: "#9ca3af", fontSize: 16, lineHeight: 1.7, marginBottom: 40 }}>
        Get CogniGate running locally in under 5 minutes using Docker.
      </p>

      <h2>Prerequisites</h2>
      <ul>
        <li>Docker Engine 27+</li>
        <li>Docker Compose v2+</li>
        <li>No Java or Go installation required (everything runs in containers)</li>
      </ul>

      <h2>One-Command Setup</h2>
      <CodeBlock>
        {`# Linux / macOS
git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
./setup.sh --dev --detach

# Windows (PowerShell)
git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
.\\setup.ps1 -Mode dev -Detach`}
      </CodeBlock>

      <p>The setup script automatically:</p>
      <ol>
        <li>Copies <code>.env.example</code> → <code>.env</code></li>
        <li>Generates a secure <code>ENCRYPTION_MASTER_KEY</code></li>
        <li>Builds and starts all 4 containers (Gateway, Analytics, PostgreSQL, Redis)</li>
      </ol>

      <h2>Verify It&apos;s Running</h2>
      <CodeBlock>{`# Check gateway health
curl http://localhost:8080/health
# → OK`}</CodeBlock>

      <h2>Your First AI Call</h2>
      <p>
        First, create a tenant and add an API key. Then route traffic through CogniGate:
      </p>
      <CodeBlock>
        {`# 1. Create tenant
curl -X POST "http://localhost:8081/api/admin/tenants?name=my-org"

# 2. Add provider key
curl -X POST http://localhost:8081/api/admin/tenants/1/keys \\
  -H "Content-Type: application/json" \\
  -d '{"providerName":"openai","apiKey":"sk-proj-..."}'

# 3. Add routing rule
curl -X POST http://localhost:8081/api/admin/tenants/1/rules \\
  -H "Content-Type: application/json" \\
  -d '{"modelName":"gpt-4","backupModelName":"gpt-3.5-turbo","priority":1}'

# 4. Make your first call
curl -X POST http://localhost:8080/v1/chat/completions \\
  -H "Authorization: Bearer <your-cg-key>" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hello!"}]}'`}
      </CodeBlock>

      <div
        style={{
          marginTop: 40,
          padding: "16px 20px",
          background: "rgba(6,182,212,0.06)",
          border: "1px solid rgba(6,182,212,0.2)",
          borderRadius: 10,
          fontSize: 14,
          color: "#9ca3af",
          lineHeight: 1.65,
        }}
      >
        <strong style={{ color: "#06b6d4" }}>Next Steps:</strong> Read the{" "}
        <a href="/docs/architecture" style={{ color: "#06b6d4" }}>Architecture Overview</a> to understand how CogniGate routes requests,
        or jump into the{" "}
        <a href="/docs/api" style={{ color: "#06b6d4" }}>API Reference</a> for all endpoints.
      </div>
    </article>
  );
}

function CodeBlock({ children }: { children: string }) {
  return (
    <pre
      style={{
        background: "#0a0e1a",
        border: "1px solid #1f2937",
        borderRadius: 10,
        padding: "16px 20px",
        fontFamily: "monospace",
        fontSize: 13,
        lineHeight: 1.7,
        color: "#e2e8f0",
        overflowX: "auto",
        margin: "16px 0",
        whiteSpace: "pre-wrap",
        wordBreak: "break-all",
      }}
    >
      {children}
    </pre>
  );
}
