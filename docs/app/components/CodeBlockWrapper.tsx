"use client";

import React, { useState } from "react";
import { Copy, Check } from "lucide-react";

type CodeBlockWrapperProps = React.DetailedHTMLProps<
    React.HTMLAttributes<HTMLPreElement>,
    HTMLPreElement
> & { language?: string };

export default function CodeBlockWrapper({
    children,
    language,
    className,
    ...props
}: CodeBlockWrapperProps) {
    const [copied, setCopied] = useState(false);

    const handleCopy = () => {
        let text = "";
        const extractText = (node: React.ReactNode): string => {
            if (typeof node === "string") return node;
            if (Array.isArray(node)) return node.map(extractText).join("");
            if (
                React.isValidElement<{ children?: React.ReactNode }>(node) &&
                node.props.children
            )
                return extractText(node.props.children);
            return "";
        };

        text = extractText(children);
        navigator.clipboard.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 2000);
    };

    return (
        <div style={{ position: "relative", margin: "24px 0" }}>
            {language && (
                <div
                    style={{
                        position: "absolute",
                        top: 0,
                        right: "42px",
                        padding: "4px 10px",
                        fontSize: "11px",
                        fontWeight: 600,
                        color: "var(--accent-cyan)",
                        background: "rgba(6, 182, 212, 0.1)",
                        borderBottomLeftRadius: "8px",
                        letterSpacing: "0.05em",
                        textTransform: "uppercase",
                        zIndex: 10,
                    }}
                >
                    {language}
                </div>
            )}

            <button
                onClick={handleCopy}
                style={{
                    position: "absolute",
                    top: 0,
                    right: 0,
                    padding: "6px",
                    background: copied
                        ? "rgba(16, 185, 129, 0.15)"
                        : "rgba(6, 182, 212, 0.1)",
                    border: "none",
                    borderBottomLeftRadius: "8px",
                    borderTopRightRadius: "12px",
                    color: copied
                        ? "var(--accent-emerald)"
                        : "var(--text-secondary)",
                    cursor: "pointer",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    transition: "all 0.2s",
                    zIndex: 10,
                }}
                title="Copy code"
            >
                {copied ? <Check size={14} /> : <Copy size={14} />}
            </button>

            <pre
                className={`code-block ${className || ""}`}
                style={{ margin: 0, paddingTop: language ? "32px" : "20px" }}
                {...props}
            >
                {children}
            </pre>
        </div>
    );
}
