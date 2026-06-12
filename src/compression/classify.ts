export type OutputPolicy = "passthrough" | "verbatim" | "compressible";

const PASSTHROUGH_PATTERNS = [
  /^auth\s+login\b/i,
  /^auth\s+logout\b/i,
  /^auth\s+status\b/i,
  /^npm\s+login\b/i,
  /^pnpm\s+login\b/i,
  /^yarn\s+login\b/i,
  /^ssh\s/i,
  /^scp\s/i,
  /^rsync\s/i,
  /^docker\s+exec\b/i,
  /^kubectl\s+exec\b/i,
  /\bvim\s/i,
  /\bnano\s/i,
  /\bless\s/i,
  /\bmore\s/i,
  /\bman\s/i,
  /^git\s+commit\b/i,
  /^npm\s+run\s+dev\b/i,
  /^pnpm\s+dev\b/i,
  /^pnpm\s+run\s+dev\b/i,
  /^yarn\s+dev\b/i,
  /^cargo\s+watch\b/i,
  /^next\s+dev\b/i,
  /^vite\b/i,
  /^vitepress\b/i,
  /^astro\s+dev\b/i,
  /^nuxt\s+dev\b/i,
  /^webpack\s+serve\b/i,
];

const VERBATIM_PATTERNS = [
  /^curl\s/i,
  /^wget\s/i,
  /^gh\s+api\b/i,
  /^glab\s+api\b/i,
  /^jq\s/i,
  /^yq\s/i,
  /\bcat\s/i,
  /\bhead\s/i,
  /\btail\s/i,
  /^docker\s+inspect\b/i,
  /^kubectl\s+get\b/i,
  /^terraform\s+show\b/i,
  /^aws\s+.*\b--output\s+json\b/i,
];

export function classifyCommand(command: string): OutputPolicy {
  const trimmed = command.trim();

  for (const pattern of PASSTHROUGH_PATTERNS) {
    if (pattern.test(trimmed)) return "passthrough";
  }

  for (const pattern of VERBATIM_PATTERNS) {
    if (pattern.test(trimmed)) return "verbatim";
  }

  return "compressible";
}

export function isProtectedPolicy(policy: OutputPolicy): boolean {
  return policy === "passthrough" || policy === "verbatim";
}
