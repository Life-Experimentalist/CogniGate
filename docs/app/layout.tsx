import type { Metadata } from "next";
import { Inter } from "next/font/google";
import "./globals.css";

const inter = Inter({
    variable: "--font-inter",
    subsets: ["latin"],
    display: "swap",
});

export const metadata: Metadata = {
    metadataBase: new URL("https://cognigate.vkrishan04.me"),
    title: {
        default:
            "CogniGate — The Zero-Downtime Cognitive Router for Enterprise AI",
        template: "%s | CogniGate",
    },
    description:
        "Self-hosted, multi-tenant AI infrastructure platform. OpenAI-compatible gateway with zero-downtime key rotation, circuit-breaking, AES-256 key vaulting, and hot-swap plugin compilation.",
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
        "project loom",
        "zero trust keys",
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
        url: "https://cognigate.vkrishan04.me",
        siteName: "CogniGate",
        title: "CogniGate — The Zero-Downtime Cognitive Router for Enterprise AI",
        description:
            "Self-hosted, multi-tenant AI infrastructure platform. Drop-in OpenAI replacement with enterprise security.",
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
                <link rel="icon" href="/logo.png" />
            </head>
            <body className="antialiased">{children}</body>
        </html>
    );
}
