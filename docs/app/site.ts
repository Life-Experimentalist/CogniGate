/**
 * Where this build believes it will be served from.
 *
 * Two knobs, both set by .github/workflows/docs.yml, because a static export
 * has to bake absolute URLs into the sitemap, robots.txt and the OpenGraph
 * tags at build time -- there is no request to read a Host header from.
 *
 * The defaults are the GitHub Pages project URL, which is where the site
 * lives until the custom domain's DNS record exists. Moving to the custom
 * domain is a two-line change in that workflow -- SITE_ORIGIN to the new
 * host, BASE_PATH to empty, since a custom domain serves from the root --
 * plus a public/CNAME file. Nothing else in the app hardcodes a hostname.
 */
const BASE_PATH = process.env.NEXT_PUBLIC_BASE_PATH ?? "";

export const SITE_ORIGIN =
    process.env.NEXT_PUBLIC_SITE_ORIGIN ?? "https://cognigate.vkrishna04.me";

/** Origin plus base path: the prefix every public URL on this site starts with. */
export const SITE_URL = `${SITE_ORIGIN}${BASE_PATH}`;

/** Prefix for files served out of public/ -- installers, prompt.md, images. */
export const asset = (path: string) => `${BASE_PATH}${path}`;

/** Absolute form of the same, for anything a user copies out of the page. */
export const publicUrl = (path: string) => `${SITE_URL}${path}`;
