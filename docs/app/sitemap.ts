import { MetadataRoute } from "next";
import { SITE_URL } from "./site";

export const dynamic = "force-static";

export default function sitemap(): MetadataRoute.Sitemap {
    const baseUrl = SITE_URL;
    const docs = [
        "",
        "/docs/getting-started",
        "/docs/configuration",
        "/docs/architecture",
        "/docs/explorer",
        "/docs/routing",
        "/docs/api",
        "/docs/integration",
        "/docs/agents",
        "/docs/security",
        "/docs/privacy",
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
