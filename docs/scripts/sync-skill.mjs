// Copies the agent skill into docs/public so the built site serves it at
// /skill/SKILL.md, which is the URL /docs/agents advertises and the one-line
// install curls.
//
// The file itself lives at plugins/cognigate/skills/cognigate/SKILL.md,
// because that path is what makes the repository installable as a Claude Code
// plugin — `/plugin marketplace add Life-Experimentalist/CogniGate` clones the
// repo and reads it from there. Keeping a second copy under docs/public and
// editing both by hand would work right up until someone edited one of them,
// so the served copy is generated and gitignored instead.

import { copyFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const docsRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const repoRoot = dirname(docsRoot);

const source = join(
    repoRoot,
    "plugins",
    "cognigate",
    "skills",
    "cognigate",
    "SKILL.md",
);
const destination = join(docsRoot, "public", "skill", "SKILL.md");

mkdirSync(dirname(destination), { recursive: true });
copyFileSync(source, destination);

console.log(`skill: ${source} -> ${destination}`);
