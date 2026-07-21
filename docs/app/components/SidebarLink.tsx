"use client";

import Link from "next/link";

export function SidebarLink({
    href,
    children,
}: {
    href: string;
    children: React.ReactNode;
}) {
    return (
        <Link
            href={href}
            className="sidebar-link"
            style={{
                display: "block",
                padding: "6px 8px",
                borderRadius: 6,
                color: "var(--text-secondary)",
                textDecoration: "none",
                fontSize: 14,
                transition: "all 0.15s",
            }}
        >
            {children}
        </Link>
    );
}
