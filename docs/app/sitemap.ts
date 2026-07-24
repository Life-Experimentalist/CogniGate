import { MetadataRoute } from "next";

export const dynamic = "force-static";

export default function sitemap(): MetadataRoute.Sitemap {
    const baseUrl = "https://cognigate.vkrishna04.me";
    const docs = [
        "",
        "/docs/getting-started",
        "/docs/configuration",
        "/docs/architecture",
        "/docs/explorer",
        "/docs/plugins",
        "/docs/routing",
        "/docs/api",
        "/docs/security",
        "/docs/billing",
        "/docs/deployment",
        "/docs/troubleshooting",
    ];

    return docs.map((route) => ({
        url: `${baseUrl}${route}`,
        lastModified: new Date(),
        changeFrequency: "weekly",
        priority: route === "" ? 1.0 : 0.8,
    }));
}
