// Pages-only snapshot generator. This file never runs in visitors' browsers.
// Its input is the authenticated GitHub Releases API response fetched by CI.
import { readFileSync, writeFileSync } from 'node:fs'
import { publishedStableInstall, publishedStableTag, requiredPublishedAssets } from '../src/release.ts'

const [inputPath, outputPath, mode] = process.argv.slice(2)
if (!inputPath || !outputPath || (mode && mode !== '--disable-on-invalid')) {
  throw new Error('Usage: node scripts/generate-release-snapshot.mjs <release.json> <dist/quickstart-release.json> [--disable-on-invalid]')
}

/**
 * GitHub Pages must *replace* any previously published active Copy command
 * when a release was unpublished/deleted or the API temporarily fails.
 * A failed deploy leaves the old dist files live, so Pages may explicitly
 * ask for an inert snapshot instead of aborting the publication. All
 * verification still stays strict for release promotion and unit tests.
 */
const failClosed = mode === '--disable-on-invalid'
const disableQuickStart = reason => {
  if (!failClosed) throw new Error(reason)
  writeFileSync(outputPath, JSON.stringify({ tag_name: null }) + '\n')
  console.warn('[Quick Start] DISABLED: ' + reason)
}
let payload
try {
  payload = JSON.parse(readFileSync(inputPath, 'utf8'))
} catch (error) {
  disableQuickStart('Release metadata unavailable or not valid JSON')
  process.exit(0)
}
const tag = publishedStableTag(payload)
if (!tag) {
  disableQuickStart('GitHub did not return a recognized published stable release')
  process.exit(0)
}

// The archived MVP must stay visibly disabled. This is a safe sentinel, not
// a historical installer or an automatically upgraded release.
if (tag === 'v0.1.0') {
  writeFileSync(outputPath, JSON.stringify({ tag_name: tag }) + '\n')
  console.log('[Quick Start] Legacy v0.1.0: public installation stays disabled')
  process.exit(0)
}

const install = publishedStableInstall(payload)
if (!install || !Array.isArray(payload.assets)) {
  disableQuickStart('Published stable release is missing a complete asset set or install.sh SHA-256')
  process.exit(0)
}
const required = requiredPublishedAssets(tag)
const assets = required.map(name => {
  const asset = payload.assets.find(value => value?.name === name)
  if (!asset || asset.state !== 'uploaded' || !Number.isFinite(asset.size) || asset.size <= 0) {
    disableQuickStart('Required release asset not uploaded: ' + name)
    process.exit(0)
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
  disableQuickStart('Generated snapshot failed browser release verifier')
  process.exit(0)
}
writeFileSync(outputPath, JSON.stringify(snapshot) + '\n')
console.log('[Quick Start] Published verified release snapshot ready: ' + tag)
