"use client";

import React from "react";

const GITHUB = "https://github.com/Life-Experimentalist/CogniGate";

export function Footer() {
    return (
        <footer
            style={{
                borderTop: "1px solid var(--border)",
                padding: "24px",
                textAlign: "center",
                background: "var(--bg-secondary)",
                width: "100%",
            }}
        >
            <div
                style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    gap: 12,
                    marginBottom: 16,
                }}
            >
                <img
                    src={`${process.env.NEXT_PUBLIC_BASE_PATH ?? ""}/logo.png`}
                    alt="CogniGate Logo"
                    style={{
                        width: 24,
                        height: 24,
                        borderRadius: 6,
                        objectFit: "cover",
                    }}
                />
                <span style={{ fontWeight: 700, color: "#f9fafb" }}>
                    CogniGate
                </span>
            </div>

            <p style={{ color: "#4b5563", fontSize: 13, marginBottom: 24 }}>
                Copyright 2026 VKrishna04 and Life Experimentalist · Apache
                License 2.0
            </p>

            <div
                style={{
                    display: "flex",
                    justifyContent: "center",
                    gap: 24,
                    flexWrap: "wrap",
                }}
            >
                {[
                    ["GitHub", GITHUB],
                    [
                        "Documentation",
                        `${process.env.NEXT_PUBLIC_BASE_PATH ?? ""}/docs/getting-started`,
                    ],
                    [
                        "Contributing",
                        `${GITHUB}/blob/main/.github/CONTRIBUTING.md`,
                    ],
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
