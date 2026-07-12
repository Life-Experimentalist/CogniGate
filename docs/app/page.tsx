"use client";

import dynamic from "next/dynamic";
import { useState } from "react";

const ParticleNetwork = dynamic(() => import("./components/ParticleNetwork"), {
  ssr: false,
});

const GITHUB = "https://github.com/Life-Experimentalist/CogniGate";

// ==================== NAV ====================
function Nav() {
  return (
    <nav
      style={{
        position: "fixed",
        top: 0,
        left: 0,
        right: 0,
        zIndex: 100,
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        padding: "14px 48px",
        background: "rgba(3,7,18,0.85)",
        backdropFilter: "blur(20px)",
        borderBottom: "1px solid rgba(31,41,55,0.6)",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
        <div
          style={{
            width: 34,
            height: 34,
            borderRadius: 9,
            background: "linear-gradient(135deg, #06b6d4, #7c3aed)",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            fontWeight: 800,
            fontSize: 13,
            color: "white",
            letterSpacing: "-0.5px",
          }}
        >
          CG
        </div>
        <span style={{ fontWeight: 700, fontSize: 16, color: "#f9fafb" }}>CogniGate</span>
      </div>

      <div style={{ display: "flex", alignItems: "center", gap: 32 }}>
        {[
          ["Docs", "/docs"],
          ["API", "/docs/api"],
          ["Plugins", "/docs/plugins"],
          ["GitHub", GITHUB],
        ].map(([label, href]) => (
          <a key={label} href={href} className="nav-link">
            {label}
          </a>
        ))}
      </div>

      <a
        href={GITHUB}
        target="_blank"
        rel="noopener noreferrer"
        style={{
          display: "inline-flex",
          alignItems: "center",
          gap: 8,
          background: "rgba(255,255,255,0.06)",
          border: "1px solid rgba(255,255,255,0.1)",
          borderRadius: 8,
          padding: "7px 16px",
          color: "#f9fafb",
          textDecoration: "none",
          fontSize: 13,
          fontWeight: 600,
          transition: "all 0.2s",
        }}
        onMouseEnter={(e) => {
          (e.currentTarget as HTMLElement).style.borderColor = "rgba(6,182,212,0.5)";
          (e.currentTarget as HTMLElement).style.background = "rgba(6,182,212,0.08)";
        }}
        onMouseLeave={(e) => {
          (e.currentTarget as HTMLElement).style.borderColor = "rgba(255,255,255,0.1)";
          (e.currentTarget as HTMLElement).style.background = "rgba(255,255,255,0.06)";
        }}
      >
        <GitHubIcon />
        Star on GitHub
      </a>
    </nav>
  );
}

// ==================== HERO ====================
function Hero() {
  const [copied, setCopied] = useState(false);

  const copySetup = () => {
    navigator.clipboard.writeText("./setup.sh --dev --detach");
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <section
      style={{
        position: "relative",
        minHeight: "100vh",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        textAlign: "center",
        padding: "120px 24px 80px",
        overflow: "hidden",
      }}
    >
      <ParticleNetwork />

      {/* Glow blobs */}
      <div
        style={{
          position: "absolute",
          top: "20%",
          left: "20%",
          width: 400,
          height: 400,
          background: "radial-gradient(circle, rgba(6,182,212,0.08) 0%, transparent 70%)",
          pointerEvents: "none",
        }}
      />
      <div
        style={{
          position: "absolute",
          bottom: "20%",
          right: "20%",
          width: 500,
          height: 500,
          background: "radial-gradient(circle, rgba(124,58,237,0.08) 0%, transparent 70%)",
          pointerEvents: "none",
        }}
      />

      <div style={{ position: "relative", zIndex: 1, maxWidth: 820 }}>
        <div className="stat-badge" style={{ marginBottom: 24 }}>
          <span style={{ width: 6, height: 6, borderRadius: "50%", background: "#10b981", display: "inline-block" }} />
          Open Source · Apache 2.0 · v0.1.0-alpha
        </div>

        <h1
          style={{
            fontSize: "clamp(40px, 6vw, 76px)",
            fontWeight: 800,
            lineHeight: 1.08,
            letterSpacing: "-2px",
            marginBottom: 24,
            color: "#f9fafb",
          }}
        >
          The{" "}
          <span className="gradient-text">Cognitive Router</span>
          <br />
          for Enterprise AI
        </h1>

        <p
          style={{
            fontSize: "clamp(16px, 2vw, 20px)",
            color: "#9ca3af",
            lineHeight: 1.65,
            marginBottom: 48,
            maxWidth: 620,
            margin: "0 auto 48px",
          }}
        >
          Self-hosted, multi-tenant AI infrastructure. Drop-in OpenAI-compatible gateway
          with zero-downtime key rotation, circuit-breaking, AES-256 key vaulting, and
          hot-swap plugin compilation — built on Go 1.26 + Java 26.
        </p>

        <div style={{ display: "flex", gap: 16, justifyContent: "center", flexWrap: "wrap", marginBottom: 48 }}>
          <a
            href="/docs/getting-started"
            className="btn-primary"
            style={{ textDecoration: "none", display: "inline-flex", alignItems: "center", gap: 8 }}
          >
            <span>Get Started →</span>
          </a>
          <a
            href={GITHUB}
            target="_blank"
            rel="noopener noreferrer"
            className="btn-secondary"
            style={{ textDecoration: "none" }}
          >
            View on GitHub
          </a>
        </div>

        {/* One-command install */}
        <div
          style={{
            display: "inline-flex",
            alignItems: "center",
            gap: 12,
            background: "rgba(10,14,26,0.8)",
            border: "1px solid rgba(31,41,55,0.8)",
            borderRadius: 12,
            padding: "12px 20px",
            backdropFilter: "blur(8px)",
          }}
        >
          <span style={{ color: "#4b5563", fontFamily: "monospace", fontSize: 13 }}>$</span>
          <code style={{ fontFamily: "monospace", fontSize: 13, color: "#e2e8f0" }}>
            ./setup.sh --dev --detach
          </code>
          <button
            onClick={copySetup}
            title="Copy to clipboard"
            style={{
              background: "none",
              border: "none",
              cursor: "pointer",
              padding: "2px 6px",
              borderRadius: 4,
              color: copied ? "#10b981" : "#6b7280",
              fontSize: 12,
              transition: "color 0.2s",
            }}
          >
            {copied ? "✓ Copied" : "Copy"}
          </button>
        </div>
      </div>
    </section>
  );
}

// ==================== FEATURES ====================
const features = [
  {
    icon: "⚡",
    title: "Zero-Downtime Key Rotation",
    desc: "Atomic Redis-backed provider API key cycling with instant Pub/Sub cache invalidation. Your users never see a disruption.",
    color: "#06b6d4",
  },
  {
    icon: "🔄",
    title: "Circuit Breaker & Failover",
    desc: "Automatic exponential backoff on 429/5xx errors with cascading fallback to backup models. Never lose a request.",
    color: "#7c3aed",
  },
  {
    icon: "🔐",
    title: "AES-256-GCM Key Vault",
    desc: "All provider API keys encrypted at rest with a master key. Never stored in plaintext anywhere in the system.",
    color: "#10b981",
  },
  {
    icon: "🏢",
    title: "True Multi-Tenancy",
    desc: "Complete per-tenant isolation: dedicated routing rules, encrypted key vaults, billing, and rate limits.",
    color: "#f59e0b",
  },
  {
    icon: "🔌",
    title: "Hot-Swap Plugin Engine",
    desc: "Upload `.java` source at runtime — Janino compiles it in-memory via isolated ClassLoaders. Zero restart required.",
    color: "#ec4899",
  },
  {
    icon: "💰",
    title: "Enterprise Billing",
    desc: "Automatic monthly invoice generation with per-tenant token cost tracking and configurable pricing.",
    color: "#06b6d4",
  },
];

function Features() {
  return (
    <section style={{ padding: "100px 48px", background: "var(--bg-secondary)" }}>
      <div style={{ maxWidth: 1100, margin: "0 auto" }}>
        <div style={{ textAlign: "center", marginBottom: 64 }}>
          <div className="section-label">Features</div>
          <h2 style={{ fontSize: "clamp(28px, 4vw, 48px)", fontWeight: 800, letterSpacing: "-1px", color: "#f9fafb" }}>
            Everything You Need for{" "}
            <span className="gradient-text">Enterprise AI</span>
          </h2>
        </div>

        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(300px, 1fr))",
            gap: 24,
          }}
        >
          {features.map((f) => (
            <div key={f.title} className="glass-card" style={{ padding: 28 }}>
              <div
                style={{
                  width: 48,
                  height: 48,
                  borderRadius: 12,
                  background: `${f.color}18`,
                  border: `1px solid ${f.color}33`,
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  fontSize: 22,
                  marginBottom: 16,
                }}
              >
                {f.icon}
              </div>
              <h3 style={{ fontSize: 17, fontWeight: 700, color: "#f9fafb", marginBottom: 10 }}>{f.title}</h3>
              <p style={{ fontSize: 14, color: "#9ca3af", lineHeight: 1.65 }}>{f.desc}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

// ==================== ARCHITECTURE ====================
function Architecture() {
  return (
    <section style={{ padding: "100px 48px" }}>
      <div style={{ maxWidth: 1100, margin: "0 auto" }}>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 64, alignItems: "center" }}>
          <div>
            <div className="section-label">Architecture</div>
            <h2
              style={{
                fontSize: "clamp(26px, 3vw, 42px)",
                fontWeight: 800,
                letterSpacing: "-1px",
                color: "#f9fafb",
                marginBottom: 20,
              }}
            >
              Polyglot Design for{" "}
              <span className="gradient-text-emerald">Maximum Performance</span>
            </h2>
            <p style={{ fontSize: 15, color: "#9ca3af", lineHeight: 1.7, marginBottom: 32 }}>
              The edge proxy is written in Go 1.26 for nanosecond-level routing decisions.
              The domain engine runs on Java 26 with Spring Boot 4.1 and Project Loom Virtual Threads —
              handling thousands of concurrent admin operations without blocking.
            </p>

            <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
              {[
                { tech: "Go 1.26 + Fiber v2", role: "Edge Proxy — :8080", color: "#06b6d4" },
                { tech: "Java 26 + Spring Boot 4.1", role: "Domain Engine — :8081", color: "#7c3aed" },
                { tech: "Redis 7", role: "Fast-Path Cache + Pub/Sub", color: "#10b981" },
                { tech: "PostgreSQL 16", role: "Source of Truth", color: "#f59e0b" },
              ].map((item) => (
                <div
                  key={item.tech}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 14,
                    padding: "12px 16px",
                    background: "var(--bg-card)",
                    borderRadius: 10,
                    border: "1px solid var(--border)",
                  }}
                >
                  <div
                    style={{
                      width: 4,
                      height: 36,
                      borderRadius: 2,
                      background: item.color,
                      flexShrink: 0,
                    }}
                  />
                  <div>
                    <div style={{ fontWeight: 600, fontSize: 14, color: "#f9fafb" }}>{item.tech}</div>
                    <div style={{ fontSize: 12, color: "#6b7280", marginTop: 2 }}>{item.role}</div>
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div className="code-block">
            <div className="code-comment">// Drop-in OpenAI replacement</div>
            <br />
            <span className="code-keyword">curl</span> -X POST{" "}
            <span className="code-string">http://localhost:8080</span>
            {"\n  "}/v1/chat/completions \<br />
            {"  "}-H{" "}
            <span className="code-string">
              &apos;Authorization: Bearer cg-abc123&apos;
            </span>{" "}
            \<br />
            {"  "}-d &apos;&#123;
            <br />
            {"    "}<span className="code-string">&quot;model&quot;</span>:{" "}
            <span className="code-string">&quot;gpt-4&quot;</span>,<br />
            {"    "}<span className="code-string">&quot;messages&quot;</span>: [&#123;
            <br />
            {"      "}<span className="code-string">&quot;role&quot;</span>:{" "}
            <span className="code-string">&quot;user&quot;</span>,<br />
            {"      "}<span className="code-string">&quot;content&quot;</span>:{" "}
            <span className="code-string">&quot;Hello, CogniGate!&quot;</span>
            <br />
            {"    "}&#125;]<br />
            {"  "}&#125;&apos;
            <br />
            <br />
            <div className="code-comment"># CogniGate transparently routes to</div>
            <div className="code-comment"># your configured provider, rotates keys,</div>
            <div className="code-comment"># tracks usage, and records telemetry</div>
          </div>
        </div>
      </div>
    </section>
  );
}

// ==================== QUICKSTART ====================
function QuickStart() {
  return (
    <section style={{ padding: "100px 48px", background: "var(--bg-secondary)" }}>
      <div style={{ maxWidth: 860, margin: "0 auto", textAlign: "center" }}>
        <div className="section-label">Quick Start</div>
        <h2
          style={{
            fontSize: "clamp(26px, 3vw, 42px)",
            fontWeight: 800,
            letterSpacing: "-1px",
            color: "#f9fafb",
            marginBottom: 16,
          }}
        >
          Up and Running in{" "}
          <span className="gradient-text">One Command</span>
        </h2>
        <p style={{ color: "#9ca3af", marginBottom: 48, fontSize: 16 }}>
          Prerequisites: Docker and Docker Compose. No Java or Go installation needed.
        </p>

        <div style={{ display: "flex", flexDirection: "column", gap: 20, textAlign: "left" }}>
          {[
            {
              step: "01",
              title: "Clone & Start",
              code: `git clone https://github.com/Life-Experimentalist/CogniGate.git
cd CogniGate
./setup.sh --dev --detach       # Linux/macOS
# or: .\\setup.ps1 -Mode dev -Detach   # Windows`,
            },
            {
              step: "02",
              title: "Create a Tenant",
              code: `curl -X POST "http://localhost:8081/api/admin/tenants?name=my-org"
# → {"id":1,"name":"my-org","cognigateApiKey":"cg-..."}`,
            },
            {
              step: "03",
              title: "Add Your Provider Key",
              code: `curl -X POST http://localhost:8081/api/admin/tenants/1/keys \\
  -H "Content-Type: application/json" \\
  -d '{"providerName":"openai","apiKey":"sk-proj-..."}'`,
            },
            {
              step: "04",
              title: "Start Routing AI Traffic",
              code: `curl -X POST http://localhost:8080/v1/chat/completions \\
  -H "Authorization: Bearer cg-..." \\
  -H "Content-Type: application/json" \\
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hello!"}]}'`,
            },
          ].map((item) => (
            <div key={item.step} style={{ display: "flex", gap: 20 }}>
              <div
                style={{
                  flexShrink: 0,
                  width: 40,
                  height: 40,
                  borderRadius: 10,
                  background: "linear-gradient(135deg, #06b6d4, #7c3aed)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  fontSize: 12,
                  fontWeight: 800,
                  color: "white",
                  marginTop: 2,
                }}
              >
                {item.step}
              </div>
              <div style={{ flex: 1 }}>
                <h3 style={{ fontSize: 15, fontWeight: 700, color: "#f9fafb", marginBottom: 10 }}>{item.title}</h3>
                <div className="code-block" style={{ fontSize: 12 }}>
                  <pre style={{ margin: 0, whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{item.code}</pre>
                </div>
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

// ==================== STATS ====================
function Stats() {
  return (
    <section
      style={{
        padding: "80px 48px",
        background: "linear-gradient(135deg, rgba(6,182,212,0.05) 0%, rgba(124,58,237,0.05) 100%)",
        borderTop: "1px solid var(--border)",
        borderBottom: "1px solid var(--border)",
      }}
    >
      <div
        style={{
          maxWidth: 900,
          margin: "0 auto",
          display: "grid",
          gridTemplateColumns: "repeat(4, 1fr)",
          gap: 32,
          textAlign: "center",
        }}
      >
        {[
          { value: "< 1ms", label: "Gateway Routing Latency" },
          { value: "AES-256", label: "Key Encryption Standard" },
          { value: "100%", label: "OpenAI API Compatible" },
          { value: "∞", label: "Tenant Scalability" },
        ].map((s) => (
          <div key={s.label}>
            <div
              className="gradient-text"
              style={{ fontSize: "clamp(28px, 4vw, 44px)", fontWeight: 800, marginBottom: 8 }}
            >
              {s.value}
            </div>
            <div style={{ fontSize: 13, color: "#6b7280" }}>{s.label}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

// ==================== FOOTER ====================
function Footer() {
  return (
    <footer
      style={{
        borderTop: "1px solid var(--border)",
        padding: "48px",
        textAlign: "center",
        background: "var(--bg-secondary)",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", justifyContent: "center", gap: 10, marginBottom: 16 }}>
        <div
          style={{
            width: 28,
            height: 28,
            borderRadius: 7,
            background: "linear-gradient(135deg, #06b6d4, #7c3aed)",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            fontWeight: 800,
            fontSize: 11,
            color: "white",
          }}
        >
          CG
        </div>
        <span style={{ fontWeight: 700, color: "#f9fafb" }}>CogniGate</span>
      </div>

      <p style={{ color: "#4b5563", fontSize: 13, marginBottom: 24 }}>
        Copyright 2026 VKrishna04 and Life Experimentalist · Apache License 2.0
      </p>

      <div style={{ display: "flex", justifyContent: "center", gap: 24 }}>
        {[
          ["GitHub", GITHUB],
          ["Documentation", "/docs"],
          ["Contributing", `${GITHUB}/blob/main/.github/CONTRIBUTING.md`],
          ["Security", `${GITHUB}/blob/main/.github/SECURITY.md`],
          ["Releases", `${GITHUB}/releases`],
        ].map(([label, href]) => (
          <a
            key={label}
            href={href}
            className="nav-link"
            style={{ fontSize: 13 }}
          >
            {label}
          </a>
        ))}
      </div>
    </footer>
  );
}

// ==================== GITHUB ICON ====================
function GitHubIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor">
      <path d="M12 0C5.37 0 0 5.37 0 12c0 5.3 3.44 9.8 8.21 11.39.6.11.82-.26.82-.58 0-.28-.01-1.03-.01-2.02-3.34.73-4.04-1.61-4.04-1.61-.55-1.39-1.33-1.76-1.33-1.76-1.09-.74.08-.73.08-.73 1.2.08 1.84 1.24 1.84 1.24 1.07 1.83 2.8 1.3 3.49.99.11-.78.42-1.3.76-1.6-2.67-.3-5.47-1.33-5.47-5.93 0-1.31.47-2.38 1.24-3.22-.12-.31-.54-1.52.12-3.18 0 0 1.01-.32 3.3 1.23a11.5 11.5 0 0 1 3-.4c1.02 0 2.04.13 3 .4 2.28-1.55 3.29-1.23 3.29-1.23.66 1.66.24 2.87.12 3.18.77.84 1.24 1.91 1.24 3.22 0 4.61-2.81 5.63-5.48 5.92.43.37.81 1.1.81 2.22 0 1.6-.01 2.9-.01 3.29 0 .32.21.7.82.58C20.56 21.8 24 17.3 24 12c0-6.63-5.37-12-12-12z" />
    </svg>
  );
}

// ==================== MAIN PAGE ====================
export default function Home() {
  return (
    <main>
      <Nav />
      <Hero />
      <Stats />
      <Features />
      <Architecture />
      <QuickStart />
      <Footer />
    </main>
  );
}
