"use client";

import { useState } from "react";

import { SidebarLink } from "../components/SidebarLink";
import { Nav } from "../components/Nav";
import { Footer } from "../components/Footer";

export default function DocsLayout({
    children,
}: {
    children: React.ReactNode;
}) {
    const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

    return (
        <div
            style={{
                minHeight: "100vh",
                display: "flex",
                flexDirection: "column",
                background: "var(--bg-primary)",
            }}
        >
            {/* Floating navigation header */}
            <Nav />

            {/* Main docs container */}
            <div
                className="docs-layout-wrapper"
                style={{
                    display: "flex",
                    flex: 1,
                    paddingTop: "90px",
                    maxWidth: "1400px",
                    width: "100%",
                    margin: "0 auto",
                }}
            >
                {/* Sidebar - Desktop */}
                <aside
                    className="docs-sidebar-desktop"
                    style={{
                        width: 230,
                        flexShrink: 0,
                        background: "transparent",
                        borderRight: "1px solid var(--border)",
                        padding: "32px 20px 32px 0",
                        position: "sticky",
                        top: "90px",
                        height: "calc(100vh - 90px)",
                        overflowY: "auto",
                    }}
                >
                    <nav>
                        <SidebarContent />
                    </nav>
                </aside>

                {/* Floating Mobile Sidebar Toggle Button */}
                <button
                    onClick={() => setMobileMenuOpen(true)}
                    style={{
                        position: "fixed",
                        bottom: "24px",
                        right: "24px",
                        zIndex: 90,
                        background:
                            "linear-gradient(135deg, var(--accent-cyan), var(--accent-violet))",
                        color: "white",
                        border: "none",
                        borderRadius: "50%",
                        width: "56px",
                        height: "56px",
                        display: "none", // Will override in JS for display on mobile
                        alignItems: "center",
                        justifyContent: "center",
                        boxShadow: "0 4px 20px rgba(6, 182, 212, 0.4)",
                        cursor: "pointer",
                    }}
                    className="mobile-toggle-btn"
                >
                    <MenuIcon />
                </button>

                {/* Mobile Menu Drawer Overlay */}
                {mobileMenuOpen && (
                    <div
                        style={{
                            position: "fixed",
                            inset: 0,
                            zIndex: 150,
                            display: "flex",
                        }}
                    >
                        {/* Backdrop */}
                        <div
                            onClick={() => setMobileMenuOpen(false)}
                            style={{
                                position: "absolute",
                                inset: 0,
                                background: "rgba(3, 7, 18, 0.75)",
                                backdropFilter: "blur(4px)",
                            }}
                        />

                        {/* Drawer Body */}
                        <div
                            style={{
                                position: "relative",
                                width: "280px",
                                height: "100%",
                                background: "var(--bg-secondary)",
                                borderRight: "1px solid var(--border)",
                                padding: "24px",
                                display: "flex",
                                flexDirection: "column",
                                overflowY: "auto",
                                boxShadow: "10px 0 30px rgba(0, 0, 0, 0.5)",
                            }}
                        >
                            <div
                                style={{
                                    display: "flex",
                                    alignItems: "center",
                                    justifyContent: "space-between",
                                    marginBottom: "24px",
                                }}
                            >
                                <span
                                    style={{
                                        fontWeight: 700,
                                        fontSize: 16,
                                        color: "#f9fafb",
                                    }}
                                >
                                    Documentation Menu
                                </span>
                                <button
                                    onClick={() => setMobileMenuOpen(false)}
                                    style={{
                                        background: "transparent",
                                        border: "none",
                                        color: "var(--text-secondary)",
                                        cursor: "pointer",
                                    }}
                                >
                                    <CloseIcon />
                                </button>
                            </div>
                            <nav onClick={() => setMobileMenuOpen(false)}>
                                <SidebarContent />
                            </nav>
                        </div>
                    </div>
                )}

                {/* Content & Right Sidebar Container */}
                <div
                    style={{
                        flex: 1,
                        display: "flex",
                        flexDirection: "column",
                        minWidth: 0,
                    }}
                >
                    <div style={{ display: "flex", flex: 1, minWidth: 0 }}>
                        <main
                            className="docs-main-container"
                            style={{
                                flex: 1,
                                padding: "40px 48px 80px",
                                maxWidth: 840,
                                lineHeight: 1.7,
                                minWidth: 0,
                            }}
                        >
                            {children}
                        </main>

                        {/* Right Sidebar (Table of Contents) */}
                        <aside
                            className="docs-right-toc"
                            style={{
                                width: 240,
                                flexShrink: 0,
                                padding: "40px 24px",
                                position: "sticky",
                                top: "90px",
                                height: "calc(100vh - 90px)",
                                overflowY: "auto",
                                borderLeft:
                                    "1px solid rgba(255, 255, 255, 0.03)",
                            }}
                        >
                            <div
                                style={{
                                    fontSize: 11,
                                    fontWeight: 700,
                                    letterSpacing: "0.12em",
                                    textTransform: "uppercase",
                                    color: "var(--text-muted)",
                                    marginBottom: 16,
                                }}
                            >
                                On this page
                            </div>
                            <nav
                                style={{
                                    display: "flex",
                                    flexDirection: "column",
                                    gap: 10,
                                    fontSize: 13,
                                }}
                            >
                                <a
                                    href="#"
                                    style={{
                                        color: "var(--accent-cyan)",
                                        textDecoration: "none",
                                    }}
                                >
                                    Overview
                                </a>
                                <a
                                    href="#"
                                    style={{
                                        color: "var(--text-secondary)",
                                        textDecoration: "none",
                                    }}
                                >
                                    Implementation
                                </a>
                                <a
                                    href="#"
                                    style={{
                                        color: "var(--text-secondary)",
                                        textDecoration: "none",
                                    }}
                                >
                                    Best Practices
                                </a>
                            </nav>
                        </aside>
                    </div>
                </div>
            </div>

            {/* Footer spans full width at the bottom */}
            <Footer />

            {/* Responsive Inline CSS overrides for Mobile Toggle Button */}
            <style jsx global>{`
                @media (max-width: 1024px) {
                    .mobile-toggle-btn {
                        display: flex !important;
                    }
                }
            `}</style>
        </div>
    );
}

function SidebarContent() {
    return (
        <>
            <SidebarSection title="Getting Started">
                <SidebarLink href="/docs/getting-started">
                    Quick Start
                </SidebarLink>
                <SidebarLink href="/docs/configuration">
                    Configuration
                </SidebarLink>
            </SidebarSection>

            <SidebarSection title="Core Concepts">
                <SidebarLink href="/docs/architecture">
                    Architecture
                </SidebarLink>
                <SidebarLink href="/docs/explorer">
                    Codebase Explorer
                </SidebarLink>
                <SidebarLink href="/docs/plugins">Plugin System</SidebarLink>
                <SidebarLink href="/docs/routing">
                    Routing & Failover
                </SidebarLink>
            </SidebarSection>

            <SidebarSection title="API Reference">
                <SidebarLink href="/docs/api">Overview</SidebarLink>
            </SidebarSection>

            <SidebarSection title="Advanced">
                <SidebarLink href="/docs/security">Security</SidebarLink>
                <SidebarLink href="/docs/billing">Billing</SidebarLink>
                <SidebarLink href="/docs/deployment">Deployment</SidebarLink>
                <SidebarLink href="/docs/troubleshooting">
                    Troubleshooting
                </SidebarLink>
            </SidebarSection>

            <SidebarSection title="Community">
                <SidebarLink href="https://github.com/Life-Experimentalist/CogniGate">
                    GitHub
                </SidebarLink>
                <SidebarLink href="https://github.com/Life-Experimentalist/CogniGate/discussions">
                    Discussions
                </SidebarLink>
                <SidebarLink href="https://github.com/Life-Experimentalist/CogniGate/releases">
                    Releases
                </SidebarLink>
            </SidebarSection>
        </>
    );
}

function SidebarSection({
    title,
    children,
}: {
    title: string;
    children: React.ReactNode;
}) {
    return (
        <div style={{ marginBottom: 24 }}>
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
            <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
                {children}
            </div>
        </div>
    );
}

function MenuIcon() {
    return (
        <svg
            width="20"
            height="20"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeLinejoin="round"
        >
            <line x1="3" y1="12" x2="21" y2="12"></line>
            <line x1="3" y1="6" x2="21" y2="6"></line>
            <line x1="3" y1="18" x2="21" y2="18"></line>
        </svg>
    );
}

function CloseIcon() {
    return (
        <svg
            width="20"
            height="20"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeLinejoin="round"
        >
            <line x1="18" y1="6" x2="6" y2="18"></line>
            <line x1="6" y1="6" x2="18" y2="18"></line>
        </svg>
    );
}
