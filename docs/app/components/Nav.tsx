"use client";

import { useState, useEffect } from "react";
import Link from "next/link";
import Image from "next/image";
import { usePathname } from "next/navigation";

const GITHUB = "https://github.com/Life-Experimentalist/CogniGate";

export function Nav() {
    const [scrolled, setScrolled] = useState(false);
    const pathname = usePathname();

    useEffect(() => {
        const handleScroll = () => {
            if (window.scrollY > 20) {
                setScrolled(true);
            } else {
                setScrolled(false);
            }
        };
        window.addEventListener("scroll", handleScroll);
        return () => window.removeEventListener("scroll", handleScroll);
    }, []);

    return (
        <nav
            style={{
                position: "fixed",
                top: scrolled ? "12px" : "20px",
                left: "50%",
                transform: "translateX(-50%)",
                width: scrolled ? "85%" : "90%",
                maxWidth: "1200px",
                zIndex: 100,
                display: "flex",
                alignItems: "center",
                justifyContent: "space-between",
                padding: scrolled ? "10px 24px" : "14px 32px",
                background: scrolled
                    ? "rgba(3, 7, 18, 0.8)"
                    : "rgba(3, 7, 18, 0.65)",
                backdropFilter: "blur(20px)",
                border: "1px solid rgba(255, 255, 255, 0.08)",
                borderRadius: "16px",
                boxShadow: scrolled
                    ? "0 10px 30px -10px rgba(6, 182, 212, 0.15), 0 1px 1px rgba(255, 255, 255, 0.05)"
                    : "0 8px 32px 0 rgba(0, 0, 0, 0.3)",
                transition: "all 0.4s cubic-bezier(0.16, 1, 0.3, 1)",
            }}
        >
            <Link
                href="/"
                style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 12,
                    textDecoration: "none",
                }}
            >
                <Image
                    src={`${process.env.NEXT_PUBLIC_BASE_PATH ?? ""}/logo.png`}
                    alt="CogniGate Logo"
                    width={32}
                    height={32}
                    style={{
                        borderRadius: 8,
                        objectFit: "cover",
                    }}
                />
                <span
                    style={{
                        fontWeight: 700,
                        fontSize: 16,
                        color: "#f9fafb",
                        letterSpacing: "-0.5px",
                    }}
                >
                    CogniGate
                </span>
            </Link>

            <div style={{ display: "flex", alignItems: "center", gap: 32 }}>
                {(() => {
                    const links: [string, string][] = [];
                    if (pathname !== "/") links.push(["Home", "/"]);
                    if (!pathname?.startsWith("/docs"))
                        links.push(["Docs", "/docs/getting-started"]);
                    links.push(["GitHub", GITHUB]);

                    return links.map(([label, href]) => (
                        <Link key={label} href={href} className="nav-link">
                            {label}
                        </Link>
                    ));
                })()}
            </div>

            <a
                href={GITHUB}
                target="_blank"
                rel="noopener noreferrer"
                className="nav-github-link"
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
            >
                <GitHubIcon />
                Star on GitHub
            </a>
        </nav>
    );
}

function GitHubIcon() {
    return (
        <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor">
            <path d="M12 0C5.37 0 0 5.37 0 12c0 5.3 3.44 9.8 8.21 11.39.6.11.82-.26.82-.58 0-.28-.01-1.03-.01-2.02-3.34.73-4.04-1.61-4.04-1.61-.55-1.39-1.33-1.76-1.33-1.76-1.09-.74.08-.73.08-.73 1.2.08 1.84 1.24 1.84 1.24 1.07 1.83 2.8 1.3 3.49.99.11-.78.42-1.3.76-1.6-2.67-.3-5.47-1.33-5.47-5.93 0-1.31.47-2.38 1.24-3.22-.12-.31-.54-1.52.12-3.18 0 0 1.01-.32 3.3 1.23a11.5 11.5 0 0 1 3-.4c1.02 0 2.04.13 3 .4 2.28-1.55 3.29-1.23 3.29-1.23.66 1.66.24 2.87.12 3.18.77.84 1.24 1.91 1.24 3.22 0 4.61-2.81 5.63-5.48 5.92.43.37.81 1.1.81 2.22 0 1.6-.01 2.9-.01 3.29 0 .32.21.7.82.58C20.56 21.8 24 17.3 24 12c0-6.63-5.37-12-12-12z" />
        </svg>
    );
}
