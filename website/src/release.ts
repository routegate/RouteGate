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

// A published tag alone does not authorize executing privileged installer code.
// GitHub's uploaded release asset digest pins the first script *before* sudo.
// The installer itself separately verifies the matching binary bundle.
export function publishedStableInstall(payload: unknown): { tag: string; installerSha256: string } | null {
  const tag = publishedStableTag(payload)
  if (!tag || tag === 'v0.1.0' || !payload || typeof payload !== 'object') return null
  const assets = (payload as { assets?: unknown }).assets
  if (!Array.isArray(assets)) return null
  const installer = assets.find(asset =>
    asset && typeof asset === 'object' && (asset as { name?: unknown }).name === 'install.sh',
  ) as { digest?: unknown } | undefined
  const digest = installer?.digest
  if (typeof digest !== 'string' || !/^sha256:[a-f0-9]{64}$/.test(digest)) return null
  return { tag, installerSha256: digest.slice('sha256:'.length) }
}

export function isSupportedQuickStartTag(tag: string): boolean {
  // Preserve historical v0.1.0 downloads, but do not automatically recommend
  // its outdated installer as the current safe first-install experience.
  return stableTagPattern.test(tag) && tag !== 'v0.1.0'
}

export function buildInstallCommand(tag: string, installerSha256: string): string {
  if (!isSupportedQuickStartTag(tag) || !/^[a-f0-9]{64}$/.test(installerSha256)) {
    throw new Error('Install command requires a verified stable release and installer SHA-256')
  }

  // A single copy/paste shell statement: temporary file, verified installer
  // *before* sudo, pinned release asset, automatic cleanup even on error.
  // Never pipe remote script bytes directly into a privileged shell.
  const url = 'https://github.com/routegate/RouteGate/releases/download/' + tag + '/install.sh'
  return "( f=$(mktemp) && trap 'rm -f \"$f\"' EXIT && curl -q -fLsS --proto '=https' --proto-redir '=https' --tlsv1.2 -o \"$f\" '" +
    url + "' && printf '%s  %s\\n' '" + installerSha256 +
    "' \"$f\" | sha256sum -c - && sudo bash \"$f\" --version '" + tag + "' )"
}
