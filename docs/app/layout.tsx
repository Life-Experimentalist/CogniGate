import type { Metadata } from "next";
import { Inter } from "next/font/google";
import "./globals.css";

const inter = Inter({
    variable: "--font-inter",
    subsets: ["latin"],
    display: "swap",
});

export const metadata: Metadata = {
    metadataBase: new URL("https://cognigate.vkrishna04.me"),
    title: {
        default:
            "CogniGate — Self-Hosted LLM Gateway with Routing, Fallback and Metering",
        template: "%s | CogniGate",
    },
    description:
        "Self-hosted, multi-tenant OpenAI-compatible LLM gateway. Capability aliases, fallback chains, circuit breakers, per-tenant quotas and durable usage metering — with provider credentials that never leave the deployment.",
    keywords: [
        "AI gateway",
        "LLM router",
        "OpenAI compatible",
        "self-hosted AI",
        "enterprise AI",
        "multi-tenant",
        "API gateway",
        "OpenRouter alternative",
        "LiteLLM alternative",
        "Go",
        "Spring Boot",
        "circuit breaker",
        "fallback routing",
        "token quotas",
        "usage metering",
    ],
    authors: [
        { name: "VKrishna04", url: "https://github.com/VKrishna04" },
        {
            name: "Life Experimentalist",
            url: "https://github.com/Life-Experimentalist",
        },
    ],
    creator: "VKrishna04 and Life Experimentalist",
    openGraph: {
        type: "website",
        locale: "en_US",
        url: "https://cognigate.vkrishna04.me",
        siteName: "CogniGate",
        title: "CogniGate — Self-Hosted LLM Gateway with Routing, Fallback and Metering",
        description:
            "A drop-in OpenAI-compatible gateway you run yourself. Your applications hold one key; your provider credentials stay in the deployment.",
        images: [
            {
                url: "/banner.png",
                width: 1200,
                height: 630,
                alt: "CogniGate Social Preview Banner",
            },
        ],
    },
    twitter: {
        card: "summary_large_image",
        title: "CogniGate — Enterprise AI Gateway",
        description:
            "Self-hosted, multi-tenant AI infrastructure platform with zero-downtime routing.",
        images: ["/banner.png"],
    },
    robots: {
        index: true,
        follow: true,
        googleBot: {
            index: true,
            follow: true,
            "max-video-preview": -1,
            "max-image-preview": "large",
            "max-snippet": -1,
        },
    },
};

export default function RootLayout({
    children,
}: Readonly<{
    children: React.ReactNode;
}>) {
    return (
        <html lang="en" className={inter.variable}>
            <head>
                <link rel="preconnect" href="https://fonts.googleapis.com" />
                <link
                    href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500&display=swap"
                    rel="stylesheet"
                />
                {/* Verification tags, icons, search scrapers compatibility */}
                <meta name="theme-color" content="#030712" />
                <link rel="icon" href={`${process.env.NEXT_PUBLIC_BASE_PATH ?? ""}/logo.png`} />
            </head>
            <body className="antialiased">{children}</body>
        </html>
    );
}
