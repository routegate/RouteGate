// The public quick-start is derived exclusively from a published GitHub
// stable release, never a moving main branch, prerelease, or hardcoded MVP.
export const stableReleaseApi = 'https://api.github.com/repos/routegate/RouteGate/releases/latest'
const stableTagPattern = /^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/

export function publishedStableTag(payload: unknown): string | null {
  if (!payload || typeof payload !== 'object' || !('tag_name' in payload)) return null
  const release = payload as {
    tag_name?: unknown
    draft?: unknown
    prerelease?: unknown
    assets?: unknown
  }
  const tag = release.tag_name
  if (typeof tag !== 'string' || !stableTagPattern.test(tag)) return null

  // Retain the historical release only to show a disabled legacy notice.
  // It is never eligible for the installation command.
  if (tag === 'v0.1.0') return tag

  // /releases/latest should return a published stable release, but also
  // validate its metadata and required uploaded assets before displaying
  // a privileged install command. A version string alone is not enough.
  if (release.draft !== false || release.prerelease !== false || !Array.isArray(release.assets)) return null
  const required = new Set([
    'install.sh',
    'SHA256SUMS',
    'INSTALLER_SHA256SUMS',
    'release-manifest.json',
    `routegate-${tag}-linux-amd64.tar.gz`,
  ])
  for (const asset of release.assets) {
    if (!asset || typeof asset !== 'object') continue
    const item = asset as { name?: unknown; size?: unknown; state?: unknown }
    if (typeof item.name === 'string' && required.has(item.name)
        && typeof item.size === 'number' && Number.isFinite(item.size)
        && item.size > 0 && item.state === 'uploaded') {
      required.delete(item.name)
    }
  }
  return required.size === 0 ? tag : null
}

export function isSupportedQuickStartTag(tag: string): boolean {
  // Preserve historical v0.1.0 downloads, but do not automatically recommend
  // its outdated installer as the current safe first-install experience.
  return stableTagPattern.test(tag) && tag !== 'v0.1.0'
}

export function buildInstallCommand(tag: string): string {
  if (!isSupportedQuickStartTag(tag)) {
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
