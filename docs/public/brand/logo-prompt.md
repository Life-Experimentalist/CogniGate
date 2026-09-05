# CogniGate — Logo Prompts

Ready-to-paste prompts for an image model, plus the constraints that make the
result usable as a real logo rather than a nice picture. Generate at the sizes
given, keep the palette, and read the checklist at the bottom before shipping
anything.

The current mark — a circuit-brain inside a padlock, cyan-to-violet on near
black — is the design language these prompts continue. If you regenerate, stay
inside it.

---

## Palette

These are the exact values the site uses. Do not let a model drift off them.

| Role | Hex |
| --- | --- |
| Background | `#030712` (near-black) |
| Surface | `#0d1117` |
| Primary accent | `#06b6d4` (cyan) |
| Secondary accent | `#7c3aed` (violet) |
| Success / live | `#10b981` (emerald) |
| Warning | `#f59e0b` (amber) |
| Text | `#f9fafb` on dark |

The signature is a **cyan → violet gradient running top-left to bottom-right**.
Everything else is monochrome.

---

## 1. Primary mark — the icon

Use this one. It is the thing that goes everywhere: favicon, README, GitHub
avatar, Docker, social. Generate at **1024×1024, transparent background**.

> A minimalist flat vector logo icon for a developer infrastructure product
> called CogniGate. A single continuous line forms a stylised gateway arch —
> two uprights and a rounded top — and inside the arch, sharing the same line
> weight, sits an abstract brain rendered as a circuit graph: four or five
> rounded lobe curves with small filled nodes at the junctions and short
> straight traces connecting them. The arch and the brain are drawn with one
> uniform stroke, roughly 6% of the canvas width, with rounded caps and joins.
> Colour is a smooth linear gradient from cyan #06b6d4 at the top-left to
> violet #7c3aed at the bottom-right, applied to the strokes only. No fill
> inside the shapes. Fully transparent background. Perfectly centred, generous
> even margin on all four sides. Geometric, precise, symmetrical, technical.
> Flat vector, no gloss, no bevel, no drop shadow, no glow, no 3D, no
> photorealism, no background panel, no text, no letters, no wordmark, no
> tagline.

**Negative prompt:** `text, letters, words, typography, tagline, watermark,
signature, drop shadow, outer glow, neon bloom, 3D render, bevel, gloss,
gradient mesh background, dark square background, busy detail, multiple
objects, cropped edges`

---

## 2. Favicon / avatar — 64px legibility test

Same icon, but simplified until it survives being 16 pixels wide. Generate at
**512×512, transparent**.

> The same CogniGate gateway-arch-and-circuit-brain icon, simplified for use at
> 16 pixels. Reduce the brain to three lobe curves and three nodes. Increase
> the stroke weight to roughly 10% of the canvas width. Keep the cyan #06b6d4
> to violet #7c3aed diagonal gradient, transparent background, and heavy even
> margin. Bold, chunky, unmistakable at thumbnail size. Flat vector. No text.

---

## 3. Wordmark lockup

Only for places with real horizontal room — a docs header or a slide. Generate
at **1600×400, transparent**.

> Horizontal logo lockup, left to right: the CogniGate gateway-arch icon, then
> a gap equal to the icon's width, then the word "CogniGate" in a clean
> geometric sans-serif with tight letter spacing, capital C and capital G, the
> rest lowercase. The wordmark is solid #f9fafb; only the icon carries the
> cyan-to-violet gradient. The cap height of the type matches the icon height
> exactly and both sit on one optical baseline. Transparent background. No
> tagline, no strapline, no descriptive text under the name, no box, no
> border.

**Do not put a tagline inside the logo file.** A tagline baked into the artwork
cannot be changed, cannot be translated, and is unreadable at every size the
icon is actually used at. Put it in the page, next to the logo.

---

## 4. Social banner (OpenGraph, 1200×630)

This is what renders when the repository is linked on social media or in chat.
Generate at **1200×630** — this one *does* have a background.

> A wide social preview banner, 1200 by 630, on a near-black #030712
> background with a very subtle dark radial vignette. Left two-thirds: the word
> "CogniGate" in a large clean geometric sans-serif in #f9fafb, and directly
> beneath it in a much smaller size, in muted grey #9ca3af, the line "One
> endpoint in front of every model your applications use". Right third: the
> gateway-arch-and-circuit-brain icon in a cyan #06b6d4 to violet #7c3aed
> gradient, with a soft cyan glow behind it. Faint thin connecting lines and
> small dots scatter across the background at low opacity, like a sparse
> network graph. Generous margins; nothing within 60 pixels of any edge.
> Clean, technical, high contrast, developer-tool aesthetic. No stock-photo
> people, no laptops, no clip art, no lorem ipsum, no extra text.

---

## Usage rules

- **The icon is the identity.** Use the icon alone anywhere under 200px. Use
  the lockup only where the name would otherwise be missing.
- **Clear space:** keep a margin equal to half the icon's height on every side.
  Nothing crowds it.
- **On light backgrounds**, the gradient still reads; do not add an outline or
  a coloured plate behind it.
- **Never** stretch it non-uniformly, recolour it outside the palette, rotate
  it, add a shadow, or place it on a busy photograph.
- **Never** regenerate the logo for a one-off use. Export from the existing
  file instead — a mark that changes shape between surfaces is not a mark.

## Before you ship a generated logo

1. Render it at 16×16 and look at it. If it is a smudge, go back to prompt 2.
2. Convert it to pure black on white. If it falls apart without colour, the
   silhouette is wrong.
3. Check the background is genuinely transparent, not a dark square. Image
   models return dark squares constantly, and it shows the moment the logo
   lands on any surface that is not exactly `#030712`.
4. Check for hallucinated text. Image models add letterforms unprompted; a
   logo with garbled pseudo-text in it is unusable.
5. Check it is centred, and that nothing is clipped at the edge.

## Where the files live

| File | Used by |
| --- | --- |
| `docs/public/logo.png` | Favicon, README, GitHub social avatar |
| `docs/public/banner.png` | README header, OpenGraph and Twitter cards |

Both are served from the docs site root — `/logo.png` and `/banner.png` — so a
replacement is a file swap and nothing else needs editing.
