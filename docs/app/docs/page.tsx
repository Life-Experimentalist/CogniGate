import type { Metadata } from "next";
import Link from "next/link";

export const metadata: Metadata = {
    title: "Documentation Overview",
    description:
        "CogniGate comprehensive documentation — getting started, core concepts, and advanced guides.",
};

const sections = [
    {
        title: "Quick Start",
        description:
            "Get CogniGate running locally in under 5 minutes using Docker.",
        href: "/docs/getting-started",
        icon: "⚡",
    },
    {
        title: "Configuration",
        description:
            "Understand the cognigate.config.yml manifest and environment variables.",
        href: "/docs/configuration",
        icon: "⚙️",
    },
    {
        title: "Architecture",
        description:
            "The two processes, what each owns, and the trade the split makes.",
        href: "/docs/architecture",
        icon: "🏗️",
    },
    {
        title: "Routing & Failover",
        description:
            "Configure capability aliases, fallback chains, and circuit breakers.",
        href: "/docs/routing",
        icon: "🔄",
    },
    {
        title: "API Reference",
        description:
            "Complete list of proxy routes and admin management endpoints.",
        href: "/docs/api",
        icon: "📖",
    },
    {
        title: "Security & Keys",
        description:
            "How keys are stored and scoped, and how the two planes are isolated.",
        href: "/docs/security",
        icon: "🔐",
    },
    {
        title: "Data & Privacy",
        description:
            "What CogniGate sees, what it records, and the one opt-in exception.",
        href: "/docs/privacy",
        icon: "🛡️",
    },
    {
        title: "Usage & Cost",
        description:
            "Non-blocking metering, durable storage, and where the cost figure comes from.",
        href: "/docs/billing",
        icon: "💳",
    },
    {
        title: "Deployment Guide",
        description:
            "What to change before this faces real traffic, and what it still owes you.",
        href: "/docs/deployment",
        icon: "🚀",
    },
    {
        title: "Troubleshooting",
        description:
            "Every error code, what it actually means, and which failures are by design.",
        href: "/docs/troubleshooting",
        icon: "🛠️",
    },
];

export default function DocsIndexPage() {
    return (
        <article style={{ maxWidth: 780 }}>
            <h1
                style={{
                    fontSize: "clamp(32px, 5vw, 42px)",
                    fontWeight: 800,
                    color: "#f9fafb",
                    marginBottom: 12,
                    letterSpacing: "-1.5px",
                    lineHeight: 1.1,
                }}
            >
                Documentation Overview
            </h1>
            <p
                style={{
                    color: "#9ca3af",
                    fontSize: 17,
                    lineHeight: 1.7,
                    marginBottom: 48,
                }}
            >
                Welcome to the CogniGate documentation. Explore guides, API
                specifications, and architectural insights to deploy and
                customize your cognitive AI routing gateway.
            </p>

            <div
                style={{
                    display: "grid",
                    gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))",
                    gap: 20,
                    marginBottom: 40,
                }}
            >
                {sections.map((s) => (
                    <Link
                        key={s.title}
                        href={s.href}
                        className="docs-card"
                        style={{
                            display: "block",
                            background: "rgba(17, 24, 39, 0.5)",
                            border: "1px solid var(--border)",
                            borderRadius: 12,
                            padding: 24,
                            textDecoration: "none",
                            transition: "all 0.2s ease-in-out",
                        }}
                    >
                        <div style={{ fontSize: 24, marginBottom: 12 }}>
                            {s.icon}
                        </div>
                        <h3
                            style={{
                                fontSize: 16,
                                fontWeight: 700,
                                color: "#f9fafb",
                                marginBottom: 8,
                            }}
                        >
                            {s.title}
                        </h3>
                        <p
                            style={{
                                fontSize: 13,
                                color: "#9ca3af",
                                lineHeight: 1.5,
                                margin: 0,
                            }}
                        >
                            {s.description}
                        </p>
                    </Link>
                ))}
            </div>
        </article>
    );
}
