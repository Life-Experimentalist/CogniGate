import type { Metadata } from "next";
import { SidebarLink } from "../components/SidebarLink";

export const metadata: Metadata = {
  title: "Documentation",
  description: "CogniGate comprehensive documentation — getting started, API reference, plugins, billing, security, and deployment guides.",
};

export default function DocsLayout({ children }: { children: React.ReactNode }) {
  return (
    <div style={{ minHeight: "100vh", display: "flex" }}>
      {/* Sidebar */}
      <aside
        style={{
          width: 260,
          flexShrink: 0,
          background: "var(--bg-secondary)",
          borderRight: "1px solid var(--border)",
          padding: "32px 20px",
          position: "sticky",
          top: 0,
          height: "100vh",
          overflowY: "auto",
        }}
      >
        <a href="/" style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 32, textDecoration: "none" }}>
          <div
            style={{
              width: 32,
              height: 32,
              borderRadius: 8,
              background: "linear-gradient(135deg, #06b6d4, #7c3aed)",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              fontSize: 14,
              fontWeight: 700,
              color: "white",
            }}
          >
            CG
          </div>
          <span style={{ fontWeight: 700, color: "var(--text-primary)", fontSize: 15 }}>CogniGate</span>
        </a>

        <nav>
          <SidebarSection title="Getting Started">
            <SidebarLink href="/docs/getting-started">Quick Start</SidebarLink>
            <SidebarLink href="/docs/configuration">Configuration</SidebarLink>
          </SidebarSection>
          <SidebarSection title="Core Concepts">
            <SidebarLink href="/docs/architecture">Architecture</SidebarLink>
            <SidebarLink href="/docs/plugins">Plugin System</SidebarLink>
            <SidebarLink href="/docs/routing">Routing & Failover</SidebarLink>
          </SidebarSection>
          <SidebarSection title="API Reference">
            <SidebarLink href="/docs/api">Overview</SidebarLink>
          </SidebarSection>
          <SidebarSection title="Advanced">
            <SidebarLink href="/docs/security">Security</SidebarLink>
            <SidebarLink href="/docs/billing">Billing</SidebarLink>
            <SidebarLink href="/docs/deployment">Deployment</SidebarLink>
            <SidebarLink href="/docs/troubleshooting">Troubleshooting</SidebarLink>
          </SidebarSection>
          <SidebarSection title="Community">
            <SidebarLink href="https://github.com/Life-Experimentalist/CogniGate">GitHub</SidebarLink>
            <SidebarLink href="https://github.com/Life-Experimentalist/CogniGate/discussions">Discussions</SidebarLink>
            <SidebarLink href="https://github.com/Life-Experimentalist/CogniGate/releases">Releases</SidebarLink>
          </SidebarSection>
        </nav>
      </aside>

      {/* Content */}
      <main style={{ flex: 1, padding: "48px 64px", maxWidth: 860, margin: "0 auto" }}>
        {children}
      </main>
    </div>
  );
}

function SidebarSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 28 }}>
      <div
        style={{
          fontSize: 11,
          fontWeight: 700,
          letterSpacing: "0.1em",
          textTransform: "uppercase",
          color: "var(--text-muted)",
          marginBottom: 10,
          paddingLeft: 8,
        }}
      >
        {title}
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>{children}</div>
    </div>
  );
}
