"use client";

import dynamic from "next/dynamic";
import { useState } from "react";
import { Nav } from "./components/Nav";
import { Footer } from "./components/Footer";

const ParticleNetwork = dynamic(() => import("./components/ParticleNetwork"), {
    ssr: false,
});

const GITHUB = "https://github.com/Life-Experimentalist/CogniGate";

// ==================== HERO ====================
function Hero() {
    const [os, setOs] = useState<"linux" | "windows" | "agentic">("linux");
    const [copied, setCopied] = useState(false);

    const getInstallCmd = () => {
        if (os === "agentic")
            return "agent run https://cognigate.vkrishna04.me/prompt.md";
        return os === "linux"
            ? "curl -sSL https://cognigate.vkrishna04.me/install.sh | bash"
            : "irm https://cognigate.vkrishna04.me/install.ps1 | iex";
    };

    const copySetup = () => {
        navigator.clipboard.writeText(getInstallCmd());
        setCopied(true);
        setTimeout(() => setCopied(false), 3000);
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

            {/* Toast Notification */}
            <div
                style={{
                    position: "fixed",
                    top: copied ? 40 : -100,
                    left: "50%",
                    transform: "translateX(-50%)",
                    background: "rgba(16, 185, 129, 0.9)",
                    backdropFilter: "blur(8px)",
                    color: "white",
                    padding: "12px 24px",
                    borderRadius: 30,
                    fontWeight: 600,
                    fontSize: 14,
                    boxShadow: "0 10px 40px rgba(16, 185, 129, 0.3)",
                    transition:
                        "top 0.4s cubic-bezier(0.175, 0.885, 0.32, 1.275)",
                    zIndex: 1000,
                    display: "flex",
                    alignItems: "center",
                    gap: 8,
                }}
            >
                <span>✓</span> Command copied to clipboard! Run it in your
                terminal.
            </div>

            {/* Glow blobs */}
            <div
                style={{
                    position: "absolute",
                    top: "20%",
                    left: "20%",
                    width: 400,
                    height: 400,
                    background:
                        "radial-gradient(circle, rgba(6,182,212,0.08) 0%, transparent 70%)",
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
                    background:
                        "radial-gradient(circle, rgba(124,58,237,0.08) 0%, transparent 70%)",
                    pointerEvents: "none",
                }}
            />

            <div style={{ position: "relative", zIndex: 1, maxWidth: 820 }}>
                <div className="stat-badge" style={{ marginBottom: 24 }}>
                    <span
                        style={{
                            width: 6,
                            height: 6,
                            borderRadius: "50%",
                            background: "#10b981",
                            display: "inline-block",
                        }}
                    />
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
                    The <span className="gradient-text">Cognitive Router</span>
                    <br />
                    for Enterprise AI
                </h1>

                <p
                    style={{
                        fontSize: "clamp(16px, 2vw, 20px)",
                        color: "#9ca3af",
                        lineHeight: 1.65,
                        marginBottom: 48,
                        maxWidth: 700,
                        margin: "0 auto 48px",
                    }}
                >
                    Self-hosted, provider-agnostic AI infrastructure for
                    multiple clients and scenarios. Host any AI model behind a
                    drop-in OpenAI-compatible gateway. Features zero-downtime
                    key rotation, circuit-breaking, AES-256 key vaulting, and
                    hot-swap plugin architecture to integrate literally any
                    provider — built on Go 1.26 + Java 25 LTS.
                </p>

                {/* Interactive Install */}
                <div
                    style={{
                        display: "flex",
                        flexDirection: "column",
                        alignItems: "center",
                        gap: 12,
                        marginBottom: 48,
                    }}
                >
                    <div
                        style={{
                            display: "flex",
                            gap: 8,
                            background: "rgba(255,255,255,0.05)",
                            padding: 4,
                            borderRadius: 20,
                        }}
                    >
                        <button
                            onClick={() => setOs("linux")}
                            style={{
                                background:
                                    os === "linux"
                                        ? "rgba(255,255,255,0.1)"
                                        : "transparent",
                                color: os === "linux" ? "#fff" : "#9ca3af",
                                border: "none",
                                padding: "6px 16px",
                                borderRadius: 16,
                                cursor: "pointer",
                                fontSize: 13,
                                fontWeight: 600,
                                transition: "all 0.2s",
                            }}
                        >
                            macOS / Linux
                        </button>
                        <button
                            onClick={() => setOs("windows")}
                            style={{
                                background:
                                    os === "windows"
                                        ? "rgba(255,255,255,0.1)"
                                        : "transparent",
                                color: os === "windows" ? "#fff" : "#9ca3af",
                                border: "none",
                                padding: "6px 16px",
                                borderRadius: 16,
                                cursor: "pointer",
                                fontSize: 13,
                                fontWeight: 600,
                                transition: "all 0.2s",
                            }}
                        >
                            Windows
                        </button>
                        <button
                            onClick={() => setOs("agentic")}
                            style={{
                                background:
                                    os === "agentic"
                                        ? "rgba(255,255,255,0.1)"
                                        : "transparent",
                                color: os === "agentic" ? "#10b981" : "#9ca3af",
                                border: "none",
                                padding: "6px 16px",
                                borderRadius: 16,
                                cursor: "pointer",
                                fontSize: 13,
                                fontWeight: 600,
                                transition: "all 0.2s",
                            }}
                        >
                            ✨ Agentic AI
                        </button>
                    </div>

                    <div
                        onClick={copySetup}
                        style={{
                            display: "inline-flex",
                            alignItems: "center",
                            gap: 12,
                            background: "rgba(10,14,26,0.8)",
                            border: "1px solid rgba(31,41,55,0.8)",
                            borderRadius: 12,
                            padding: "16px 24px",
                            backdropFilter: "blur(8px)",
                            cursor: "pointer",
                            transition: "all 0.2s",
                        }}
                        onMouseEnter={(e) => {
                            e.currentTarget.style.borderColor =
                                "var(--accent-cyan)";
                        }}
                        onMouseLeave={(e) => {
                            e.currentTarget.style.borderColor =
                                "rgba(31,41,55,0.8)";
                        }}
                    >
                        <span
                            style={{
                                color: "#4b5563",
                                fontFamily: "monospace",
                                fontSize: 14,
                            }}
                        >
                            $
                        </span>
                        <code
                            style={{
                                fontFamily: "monospace",
                                fontSize: 14,
                                color: "#e2e8f0",
                            }}
                        >
                            {getInstallCmd()}
                        </code>
                        <div
                            style={{
                                color: copied
                                    ? "#10b981"
                                    : "var(--accent-cyan)",
                                fontSize: 18,
                                marginLeft: 8,
                            }}
                        >
                            {copied ? "✓" : "❐"}
                        </div>
                    </div>
                </div>

                <div
                    style={{
                        display: "flex",
                        gap: 16,
                        justifyContent: "center",
                        flexWrap: "wrap",
                    }}
                >
                    <a
                        href="/docs/getting-started"
                        className="btn-primary"
                        style={{
                            textDecoration: "none",
                            display: "inline-flex",
                            alignItems: "center",
                            gap: 8,
                        }}
                    >
                        <span>Read the Docs →</span>
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
        title: "Any AI Model via Plugins",
        desc: "Provider-agnostic by design. Upload `.java` plugins at runtime to support local LLMs, Anthropic, Gemini, or custom models. Janino compiles it in-memory with zero restarts.",
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
        <section
            style={{ padding: "100px 48px", background: "var(--bg-secondary)" }}
        >
            <div style={{ maxWidth: 1100, margin: "0 auto" }}>
                <div style={{ textAlign: "center", marginBottom: 64 }}>
                    <div className="section-label">Features</div>
                    <h2
                        style={{
                            fontSize: "clamp(28px, 4vw, 48px)",
                            fontWeight: 800,
                            letterSpacing: "-1px",
                            color: "#f9fafb",
                        }}
                    >
                        Everything You Need for{" "}
                        <span className="gradient-text">Enterprise AI</span>
                    </h2>
                </div>

                <div
                    style={{
                        display: "grid",
                        gridTemplateColumns:
                            "repeat(auto-fit, minmax(300px, 1fr))",
                        gap: 24,
                    }}
                >
                    {features.map((f) => (
                        <div
                            key={f.title}
                            className="glass-card"
                            style={{ padding: 28 }}
                        >
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
                            <h3
                                style={{
                                    fontSize: 17,
                                    fontWeight: 700,
                                    color: "#f9fafb",
                                    marginBottom: 10,
                                }}
                            >
                                {f.title}
                            </h3>
                            <p
                                style={{
                                    fontSize: 14,
                                    color: "#9ca3af",
                                    lineHeight: 1.65,
                                }}
                            >
                                {f.desc}
                            </p>
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
                <div
                    style={{
                        display: "grid",
                        gridTemplateColumns: "1fr 1fr",
                        gap: 64,
                        alignItems: "center",
                    }}
                >
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
                            <span className="gradient-text-emerald">
                                Maximum Performance
                            </span>
                        </h2>
                        <p
                            style={{
                                fontSize: 15,
                                color: "#9ca3af",
                                lineHeight: 1.7,
                                marginBottom: 32,
                            }}
                        >
                            The edge proxy is written in Go 1.26 for
                            nanosecond-level routing decisions. The domain
                            engine runs on Java 25 with Spring Boot 4.1 and
                            Project Loom Virtual Threads — handling thousands of
                            concurrent admin operations without blocking.
                        </p>

                        <div
                            style={{
                                display: "flex",
                                flexDirection: "column",
                                gap: 16,
                            }}
                        >
                            {[
                                {
                                    tech: "Go 1.26 + Fiber v2",
                                    role: "Edge Proxy — :8080",
                                    color: "#06b6d4",
                                },
                                {
                                    tech: "Java 25 LTS + Spring Boot 4.1",
                                    role: "Domain Engine — :8081",
                                    color: "#7c3aed",
                                },
                                {
                                    tech: "Redis 7",
                                    role: "Fast-Path Cache + Pub/Sub",
                                    color: "#10b981",
                                },
                                {
                                    tech: "PostgreSQL 16",
                                    role: "Source of Truth",
                                    color: "#f59e0b",
                                },
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
                                        <div
                                            style={{
                                                fontWeight: 600,
                                                fontSize: 14,
                                                color: "#f9fafb",
                                            }}
                                        >
                                            {item.tech}
                                        </div>
                                        <div
                                            style={{
                                                fontSize: 12,
                                                color: "#6b7280",
                                                marginTop: 2,
                                            }}
                                        >
                                            {item.role}
                                        </div>
                                    </div>
                                </div>
                            ))}
                        </div>
                    </div>

                    <div className="code-block">
                        <div className="code-comment">
                            {/* Drop-in OpenAI replacement */}
                        </div>
                        <br />
                        <span className="code-keyword">curl</span> -X POST{" "}
                        <span className="code-string">
                            http://localhost:8080
                        </span>
                        {"\n  "}/v1/chat/completions \<br />
                        {"  "}-H{" "}
                        <span className="code-string">
                            &apos;Authorization: Bearer cg-abc123&apos;
                        </span>{" "}
                        \<br />
                        {"  "}-d &apos;&#123;
                        <br />
                        {"    "}
                        <span className="code-string">
                            &quot;model&quot;
                        </span>:{" "}
                        <span className="code-string">&quot;gpt-4&quot;</span>,
                        <br />
                        {"    "}
                        <span className="code-string">
                            &quot;messages&quot;
                        </span>
                        : [&#123;
                        <br />
                        {"      "}
                        <span className="code-string">
                            &quot;role&quot;
                        </span>:{" "}
                        <span className="code-string">&quot;user&quot;</span>,
                        <br />
                        {"      "}
                        <span className="code-string">&quot;content&quot;</span>
                        :{" "}
                        <span className="code-string">
                            &quot;Hello, CogniGate!&quot;
                        </span>
                        <br />
                        {"    "}&#125;]
                        <br />
                        {"  "}&#125;&apos;
                        <br />
                        <br />
                        <div className="code-comment">
                            # CogniGate transparently routes to
                        </div>
                        <div className="code-comment">
                            # your configured provider, rotates keys,
                        </div>
                        <div className="code-comment">
                            # tracks usage, and records telemetry
                        </div>
                    </div>
                </div>
            </div>
        </section>
    );
}

// ==================== QUICKSTART ====================
function QuickStart() {
    return (
        <section
            style={{ padding: "100px 48px", background: "var(--bg-secondary)" }}
        >
            <div
                style={{ maxWidth: 860, margin: "0 auto", textAlign: "center" }}
            >
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
                    Prerequisites: Docker and Docker Compose. No Java or Go
                    installation needed.
                </p>

                <div
                    style={{
                        display: "flex",
                        flexDirection: "column",
                        gap: 20,
                        textAlign: "left",
                    }}
                >
                    {[
                        {
                            step: "01",
                            title: "Install & Start",
                            code: `# Linux/macOS:
curl -sSL https://cognigate.vkrishna04.me/install.sh | bash

# Windows PowerShell:
irm https://cognigate.vkrishna04.me/install.ps1 | iex`,
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
                        <div
                            key={item.step}
                            style={{ display: "flex", gap: 20 }}
                        >
                            <div
                                style={{
                                    flexShrink: 0,
                                    width: 40,
                                    height: 40,
                                    borderRadius: 10,
                                    background:
                                        "linear-gradient(135deg, #06b6d4, #7c3aed)",
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
                                <h3
                                    style={{
                                        fontSize: 15,
                                        fontWeight: 700,
                                        color: "#f9fafb",
                                        marginBottom: 10,
                                    }}
                                >
                                    {item.title}
                                </h3>
                                <div
                                    className="code-block"
                                    style={{ fontSize: 12 }}
                                >
                                    <pre
                                        style={{
                                            margin: 0,
                                            whiteSpace: "pre-wrap",
                                            wordBreak: "break-all",
                                        }}
                                    >
                                        {item.code}
                                    </pre>
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
                background:
                    "linear-gradient(135deg, rgba(6,182,212,0.05) 0%, rgba(124,58,237,0.05) 100%)",
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
                            style={{
                                fontSize: "clamp(28px, 4vw, 44px)",
                                fontWeight: 800,
                                marginBottom: 8,
                            }}
                        >
                            {s.value}
                        </div>
                        <div style={{ fontSize: 13, color: "#6b7280" }}>
                            {s.label}
                        </div>
                    </div>
                ))}
            </div>
        </section>
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
