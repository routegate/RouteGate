import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { buildInstallCommand, isSupportedQuickStartTag, publishedStableTag, stableReleaseApi } from '../src/release.ts'

const requiredAssets = tag => [
  'install.sh',
  'SHA256SUMS',
  'INSTALLER_SHA256SUMS',
  'release-manifest.json',
  `routegate-${tag}-linux-amd64.tar.gz`,
]

const releaseFixture = (tag = 'v1.2.3') => ({
  tag_name: tag,
  draft: false,
  prerelease: false,
  assets: requiredAssets(tag).map(name => ({ name, size: 1024, state: 'uploaded' })),
})

test('published stable tag is validated and not mutable, injected or prerelease', () => {
  assert.equal(publishedStableTag({ tag_name: 'v0.1.0' }), 'v0.1.0')
  assert.equal(publishedStableTag(releaseFixture('v12.34.56')), 'v12.34.56')
  for (const malformed of [
    {}, null, undefined, [], { tag_name: 123 }, { tag_name: 'main' },
    { tag_name: 'latest' }, { tag_name: 'v0.2.0-rc.2' },
    { tag_name: 'v1.2.3;sudo rm -rf /' }, { tag_name: 'v1.02.3' },
    { tag_name: 'v0.1.0\ncurl example.com' }, { tag_name: 'refs/heads/main' },
  ]) assert.equal(publishedStableTag(malformed), null)
})

test('quick start refuses incomplete, draft, malformed or unavailable release assets', () => {
  const valid = releaseFixture()
  assert.equal(publishedStableTag(valid), 'v1.2.3')
  for (const asset of requiredAssets('v1.2.3')) {
    assert.equal(
      publishedStableTag({ ...valid, assets: valid.assets.filter(item => item.name !== asset) }),
      null,
      `missing ${asset} must disable installation`,
    )
  }
  for (const invalid of [
    { ...valid, draft: true },
    { ...valid, prerelease: true },
    { ...valid, assets: [] },
    { ...valid, assets: undefined },
    { ...valid, assets: valid.assets.map(item => item.name === 'install.sh' ? { ...item, size: 0 } : item) },
    { ...valid, assets: valid.assets.map(item => item.name === 'SHA256SUMS' ? { ...item, state: 'starter' } : item) },
    { ...valid, assets: valid.assets.map(item => item.name === 'INSTALLER_SHA256SUMS' ? { ...item, size: '9999' } : item) },
  ]) assert.equal(publishedStableTag(invalid), null)
  // A legacy release is recognized only so the UI can show its disabled warning.
  assert.equal(publishedStableTag({ tag_name: 'v0.1.0' }), 'v0.1.0')
})

test('quick start is pinned to the exact published tag', () => {
  assert.equal(
    stableReleaseApi,
    'https://api.github.com/repos/routegate/RouteGate/releases/latest',
  )
  const command = buildInstallCommand('v1.2.3')
  assert.ok(command.startsWith('VERSION=v1.2.3\n'))
  assert.match(command, /raw\.githubusercontent\.com\/routegate\/RouteGate\/\$\{VERSION\}\/install\.sh/)
  assert.match(command, /sudo bash routegate-install\.sh --version "\$\{VERSION\}"/)
  assert.ok(command.includes("curl -fL --proto '=https' --tlsv1.2"))
  assert.ok(command.includes('\nless routegate-install.sh\n'))
  assert.ok(!command.includes('curl | sudo bash'))
  assert.equal(isSupportedQuickStartTag('v0.1.0'), false)
  assert.equal(isSupportedQuickStartTag('v1.2.3'), true)
  assert.throws(() => buildInstallCommand('v0.1.0'))
  assert.throws(() => buildInstallCommand('main'))
  assert.throws(() => buildInstallCommand('v1.2.3-rc.1'))
})

test('site does not quietly fall back to a hardcoded obsolete tag', () => {
  const app = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8')
  assert.ok(!app.includes("VERSION=v0.1.0"))
  const readme = readFileSync(new URL('../../README.md', import.meta.url), 'utf8')
  assert.ok(!readme.includes('VERSION=v0.1.0'))
  assert.ok(readme.includes('VERSION=REPLACE_WITH_STABLE_TAG'))
  const prerender = readFileSync(new URL('./prerender.mjs', import.meta.url), 'utf8')
  assert.ok(!prerender.includes('Stable v0.1.0 supports'))
  assert.ok(!prerender.includes('Стабильный v0.1.0'))
  assert.match(app, /disabled=\{!installCommand\}/)
  assert.match(app, /status: 'unavailable'/)
  assert.match(app, /status: tag === 'v0.1.0' \? 'legacy' : 'ready'/)
  assert.match(app, /legacyReleaseDisabled/)
  assert.match(app, /publishedStableTag\(payload\)/)
})
