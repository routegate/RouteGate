// The public quick-start is derived exclusively from a published GitHub
// stable release, never a moving main branch, prerelease, or hardcoded MVP.
export const stableReleaseApi = 'https://api.github.com/repos/routegate/RouteGate/releases/latest'
const stableTagPattern = /^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/

export function publishedStableTag(payload: unknown): string | null {
  if (!payload || typeof payload !== 'object' || !('tag_name' in payload)) return null
  const tag = (payload as { tag_name?: unknown }).tag_name
  return typeof tag === 'string' && stableTagPattern.test(tag) ? tag : null
}

export function buildInstallCommand(tag: string): string {
  if (!stableTagPattern.test(tag)) {
    throw new Error('Install command requires a verified stable release tag')
  }
  return [
    `VERSION=${tag}`,
    "curl -fL --proto '=https' --tlsv1.2 \\",
    '  "https://raw.githubusercontent.com/routegate/RouteGate/${VERSION}/install.sh" \\',
    '  -o routegate-install.sh',
    'less routegate-install.sh',
    'sudo bash routegate-install.sh --version "${VERSION}"',
  ].join('\n')
}
