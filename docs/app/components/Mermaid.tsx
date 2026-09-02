"use client";

import { useEffect, useRef, useState } from "react";

export function Mermaid({ chart }: { chart: string }) {
    const ref = useRef<HTMLDivElement>(null);
    const [svg, setSvg] = useState<string>("");
    const [error, setError] = useState<string>("");

    useEffect(() => {
        let active = true;

        async function renderChart() {
            try {
                const mermaid = (await import("mermaid")).default;
                mermaid.initialize({
                    startOnLoad: false,
                    theme: "dark",
                    securityLevel: "loose",
                    themeVariables: {
                        background: "#0a0e1a",
                        primaryColor: "#06b6d4",
                        lineColor: "#7c3aed",
                    },
                });

                const id = `mermaid-${Math.floor(Math.random() * 1000000)}`;
                const { svg: renderedSvg } = await mermaid.render(id, chart);

                if (active) {
                    setSvg(renderedSvg);
                    setError("");
                }
            } catch (err) {
                console.error(err);
                if (active) {
                    setError(
                        err instanceof Error
                            ? err.message
                            : "Failed to render chart",
                    );
                }
            }
        }

        renderChart();

        return () => {
            active = false;
        };
    }, [chart]);

    if (error) {
        return (
            <div
                style={{
                    color: "#f87171",
                    padding: 12,
                    border: "1px solid #f87171",
                    borderRadius: 8,
                    fontSize: 13,
                    fontFamily: "monospace",
                }}
            >
                Mermaid Error: {error}
            </div>
        );
    }

    if (!svg) {
        return (
            <div style={{ color: "var(--text-muted)", fontSize: 13 }}>
                Loading diagram...
            </div>
        );
    }

    return (
        <div
            ref={ref}
            dangerouslySetInnerHTML={{ __html: svg }}
            style={{
                display: "flex",
                justifyContent: "center",
                background: "rgba(10, 14, 26, 0.4)",
                border: "1px solid var(--border)",
                borderRadius: 12,
                padding: 24,
                margin: "24px 0",
                overflowX: "auto",
            }}
        />
    );
}
