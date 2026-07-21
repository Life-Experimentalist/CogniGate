import React from "react";

export function CodeBlock({ children }: { children: string }) {
    return (
        <pre
            className="code-block"
            style={{
                margin: "20px 0",
                whiteSpace: "pre-wrap",
                wordBreak: "break-all",
                overflowX: "auto",
            }}
        >
            <code>{children}</code>
        </pre>
    );
}
