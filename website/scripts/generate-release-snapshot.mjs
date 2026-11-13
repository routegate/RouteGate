// Pages-only snapshot generator. This file never runs in visitors' browsers.
// Its input is the authenticated GitHub Releases API response fetched by CI.
import { readFileSync, writeFileSync } from 'node:fs'
import { publishedStableInstall, publishedStableTag } from '../src/release.ts'

const [inputPath, outputPath] = process.argv.slice(2)
if (!inputPath || !outputPath) {
  throw new Error('Usage: node scripts/generate-release-snapshot.mjs <release.json> <dist/quickstart-release.json>')
}

const payload = JSON.parse(readFileSync(inputPath, 'utf8'))
const tag = publishedStableTag(payload)
if (!tag) throw new Error('GitHub did not return a recognized published stable release')

// The archived MVP must stay visibly disabled. This is a safe sentinel, not
// a historical installer or an automatically upgraded release.
if (tag === 'v0.1.0') {
  writeFileSync(outputPath, JSON.stringify({ tag_name: tag }) + '\n')
  console.log('[Quick Start] Legacy v0.1.0: public installation stays disabled')
  process.exit(0)
}

const install = publishedStableInstall(payload)
if (!install || !Array.isArray(payload.assets)) {
  throw new Error('Published stable release is missing a complete asset set or install.sh SHA-256')
}
const required = [
  'install.sh', 'SHA256SUMS', 'INSTALLER_SHA256SUMS',
  'release-manifest.json', 'routegate-' + tag + '-linux-amd64.tar.gz',
]
const assets = required.map(name => {
  const asset = payload.assets.find(value => value?.name === name)
  if (!asset || asset.state !== 'uploaded' || !Number.isFinite(asset.size) || asset.size <= 0) {
    throw new Error('Required release asset not uploaded: ' + name)
  }
  return {
    name,
    state: 'uploaded',
    size: asset.size,
    ...(name === 'install.sh' ? { digest: 'sha256:' + install.installerSha256 } : {}),
  }
})
const snapshot = {
  tag_name: tag,
  draft: false,
  prerelease: false,
  assets,
}
if (!publishedStableInstall(snapshot)) {
  throw new Error('Generated snapshot must itself pass the exact browser release verifier')
}
writeFileSync(outputPath, JSON.stringify(snapshot) + '\n')
console.log('[Quick Start] Published verified release snapshot ready: ' + tag)
