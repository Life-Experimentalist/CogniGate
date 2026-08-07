import type { Metadata } from "next";
import { Inter } from "next/font/google";
import "./globals.css";

const inter = Inter({
  variable: "--font-inter",
  subsets: ["latin"],
  display: "swap",
});

const BASE_PATH = process.env.NEXT_PUBLIC_BASE_PATH || "";

export const metadata: Metadata = {
  metadataBase: new URL("https://life-experimentalist.github.io/CogniGate"),
  title: {
    default: "CogniGate — The Zero-Downtime Cognitive Router for Enterprise AI",
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
  ],
  authors: [
    { name: "VKrishna04", url: "https://github.com/VKrishna04" },
    { name: "Life Experimentalist", url: "https://github.com/Life-Experimentalist" },
  ],
  creator: "VKrishna04 and Life Experimentalist",
  openGraph: {
    type: "website",
    locale: "en_US",
    url: "https://life-experimentalist.github.io/CogniGate",
    siteName: "CogniGate",
    title: "CogniGate — The Zero-Downtime Cognitive Router for Enterprise AI",
    description:
      "Self-hosted, multi-tenant AI infrastructure platform. Drop-in OpenAI replacement with enterprise security.",
  },
  twitter: {
    card: "summary_large_image",
    title: "CogniGate — Enterprise AI Gateway",
    description: "Self-hosted, multi-tenant AI infrastructure platform.",
  },
  robots: {
    index: true,
    follow: true,
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
      </head>
      <body className="antialiased">{children}</body>
    </html>
  );
}
