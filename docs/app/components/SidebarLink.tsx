"use client";

export function SidebarLink({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <a
      href={href}
      style={{
        display: "block",
        padding: "6px 8px",
        borderRadius: 6,
        color: "var(--text-secondary)",
        textDecoration: "none",
        fontSize: 14,
        transition: "all 0.15s",
      }}
      onMouseEnter={(e) => {
        (e.target as HTMLElement).style.background = "var(--bg-card)";
        (e.target as HTMLElement).style.color = "var(--text-primary)";
      }}
      onMouseLeave={(e) => {
        (e.target as HTMLElement).style.background = "transparent";
        (e.target as HTMLElement).style.color = "var(--text-secondary)";
      }}
    >
      {children}
    </a>
  );
}
