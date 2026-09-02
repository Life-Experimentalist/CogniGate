import type { MDXComponents } from "mdx/types";
import React from "react";
// import Image from "next/image";
import Link from "next/link";
import CodeBlockWrapper from "./app/components/CodeBlockWrapper";

export function useMDXComponents(components: MDXComponents): MDXComponents {
    return {
        ...components,
        h1: ({ children }) => (
            <h1
                style={{
                    fontSize: "36px",
                    fontWeight: 800,
                    color: "var(--text-primary)",
                    marginBottom: "20px",
                    marginTop: "40px",
                    letterSpacing: "-1.5px",
                    lineHeight: "1.15",
                    borderBottom: "1px solid rgba(255,255,255,0.05)",
                    paddingBottom: "10px",
                }}
            >
                {children}
            </h1>
        ),
        h2: ({ children }) => (
            <h2
                style={{
                    fontSize: "22px",
                    fontWeight: 700,
                    color: "var(--text-primary)",
                    marginBottom: "14px",
                    marginTop: "32px",
                    letterSpacing: "-0.5px",
                    lineHeight: "1.25",
                }}
            >
                {children}
            </h2>
        ),
        h3: ({ children }) => (
            <h3
                style={{
                    fontSize: "17px",
                    fontWeight: 600,
                    color: "var(--text-primary)",
                    marginBottom: "10px",
                    marginTop: "24px",
                }}
            >
                {children}
            </h3>
        ),
        p: ({ children }) => (
            <p
                style={{
                    fontSize: "15px",
                    lineHeight: "1.75",
                    color: "var(--text-secondary)",
                    marginBottom: "18px",
                }}
            >
                {children}
            </p>
        ),
        ul: ({ children }) => (
            <ul
                style={{
                    paddingLeft: "24px",
                    marginBottom: "18px",
                    listStyleType: "disc",
                    color: "var(--text-secondary)",
                }}
            >
                {children}
            </ul>
        ),
        ol: ({ children }) => (
            <ol
                style={{
                    paddingLeft: "24px",
                    marginBottom: "18px",
                    listStyleType: "decimal",
                    color: "var(--text-secondary)",
                }}
            >
                {children}
            </ol>
        ),
        li: ({ children }) => (
            <li
                style={{
                    marginBottom: "8px",
                    fontSize: "15px",
                    lineHeight: "1.7",
                }}
            >
                {children}
            </li>
        ),
        pre: ({
            children,
            className,
            ...props
        }: React.DetailedHTMLProps<React.HTMLAttributes<HTMLPreElement>, HTMLPreElement>) => {
            const codeChild = (
                Array.isArray(children) ? children[0] : children
            ) as React.ReactElement<{ className?: string }>;
            const languageClass = codeChild?.props?.className || "";
            let language = languageClass.replace("language-", "");
            if (language === "undefined" || !language) language = "";

            return (
                <CodeBlockWrapper
                    language={language}
                    className={className}
                    {...props}
                >
                    {children}
                </CodeBlockWrapper>
            );
        },
        code: ({ children, className }) => {
            const isInline = !className || !className.includes("language-");
            if (isInline) {
                return (
                    <code
                        style={{
                            fontSize: "13.5px",
                            fontFamily: "monospace",
                            background: "rgba(255, 255, 255, 0.05)",
                            border: "1px solid rgba(255, 255, 255, 0.08)",
                            padding: "2px 6px",
                            borderRadius: "6px",
                            color: "var(--accent-cyan)",
                        }}
                    >
                        {children}
                    </code>
                );
            }
            return (
                <code
                    style={{
                        fontSize: "13.5px",
                        fontFamily: "monospace",
                        color: "#f9fafb",
                        background: "transparent",
                        border: "none",
                        padding: 0,
                    }}
                >
                    {children}
                </code>
            );
        },
        a: ({ children, href }) => {
            const isExternal =
                href && (href.startsWith("http") || href.startsWith("https"));
            return (
                <Link
                    href={href}
                    target={isExternal ? "_blank" : undefined}
                    rel={isExternal ? "noopener noreferrer" : undefined}
                    className="mdx-link"
                    style={{
                        color: "var(--accent-cyan)",
                        textDecoration: "none",
                        fontWeight: 500,
                        borderBottom: "1px dotted var(--accent-cyan)",
                        transition: "all 0.2s ease-in-out",
                    }}
                >
                    {children}
                </Link>
            );
        },
        blockquote: ({ children }) => {
            let isAlert = false;
            let alertType = "NOTE";

            const extractText = (node: React.ReactNode): string => {
                if (typeof node === "string") return node;
                if (Array.isArray(node)) return node.map(extractText).join("");
                if (React.isValidElement<{ children?: React.ReactNode }>(node) && node.props.children)
                    return extractText(node.props.children);
                return "";
            };

            const text = extractText(children);
            const match = text.match(
                /^\[!(TIP|WARNING|IMPORTANT|NOTE|CAUTION)\]/,
            );

            if (match) {
                isAlert = true;
                alertType = match[1];
            }

            if (!isAlert) {
                return <blockquote>{children}</blockquote>;
            }

            const alertClass = `markdown-alert markdown-alert-${alertType.toLowerCase()}`;

            let icon = "ℹ️";
            if (alertType === "TIP") icon = "💡";
            if (alertType === "WARNING") icon = "⚠️";
            if (alertType === "IMPORTANT") icon = "✨";
            if (alertType === "CAUTION") icon = "🛑";

            return (
                <blockquote className={alertClass}>
                    <div className="markdown-alert-title">
                        <span style={{ marginRight: "8px" }}>{icon}</span>
                        {alertType.charAt(0).toUpperCase() +
                            alertType.slice(1).toLowerCase()}
                    </div>
                    <div>{children}</div>
                </blockquote>
            );
        },
    };
}
