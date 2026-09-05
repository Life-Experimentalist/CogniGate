import type { NextConfig } from "next";
import createMDX from "@next/mdx";

const nextConfig: NextConfig = {
    output: "export",
    basePath: process.env.NEXT_PUBLIC_BASE_PATH || "",
    trailingSlash: true,
    images: {
        unoptimized: true,
    },
    pageExtensions: ["js", "jsx", "md", "mdx", "ts", "tsx"],
};

// GitHub Flavored Markdown. Without it a `| a | b |` table is not a table --
// remark leaves it as a paragraph of pipe characters, which is what every
// table in every page under app/docs was rendering as. Named as a string
// rather than imported: the build runs on Turbopack, which serialises this
// config across to Rust and cannot carry a JavaScript function.
const withMDX = createMDX({
    options: {
        remarkPlugins: ["remark-gfm"],
    },
});

export default withMDX(nextConfig);
