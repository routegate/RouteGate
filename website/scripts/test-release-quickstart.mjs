import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'
import { buildInstallCommand, publishedStableTag, stableReleaseApi } from '../src/release.ts'

test('published stable tag is validated and not mutable, injected or prerelease', () => {
  assert.equal(publishedStableTag({ tag_name: 'v0.1.0' }), 'v0.1.0')
  assert.equal(publishedStableTag({ tag_name: 'v12.34.56' }), 'v12.34.56')
  for (const malformed of [
    {}, null, undefined, [], { tag_name: 123 }, { tag_name: 'main' },
    { tag_name: 'latest' }, { tag_name: 'v0.2.0-rc.2' },
    { tag_name: 'v1.2.3;sudo rm -rf /' }, { tag_name: 'v1.02.3' },
    { tag_name: 'v0.1.0\ncurl example.com' }, { tag_name: 'refs/heads/main' },
  ]) assert.equal(publishedStableTag(malformed), null)
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
  assert.match(app, /publishedStableTag\(payload\)/)
})
