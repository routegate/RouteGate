import assert from 'node:assert/strict'
import { test } from 'node:test'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  buildInstallCommand,
  isSupportedQuickStartTag,
  publishedStableInstall,
  publishedStableTag,
  stableReleaseApi,
} from '../src/release.ts'

const goodDigest = 'sha256:' + 'a'.repeat(64)
const requiredAssets = tag => [
  'install.sh',
  'SHA256SUMS',
  'INSTALLER_SHA256SUMS',
  'release-manifest.json',
  'routegate-' + tag + '-linux-amd64.tar.gz',
]

const releaseFixture = (tag = 'v1.2.3') => ({
  tag_name: tag,
  draft: false,
  prerelease: false,
  assets: requiredAssets(tag).map(name => ({
    name,
    size: 1024,
    state: 'uploaded',
    digest: name === 'install.sh' ? goodDigest : undefined,
  })),
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

test('quick start refuses incomplete, draft or unverifiable release assets', () => {
  const valid = releaseFixture()
  assert.equal(publishedStableTag(valid), 'v1.2.3')
  assert.deepEqual(publishedStableInstall(valid), {
    tag: 'v1.2.3',
    installerSha256: 'a'.repeat(64),
  })
  for (const asset of requiredAssets('v1.2.3')) {
    const missing = { ...valid, assets: valid.assets.filter(item => item.name !== asset) }
    assert.equal(publishedStableTag(missing), null)
    assert.equal(publishedStableInstall(missing), null)
  }
  for (const invalid of [
    { ...valid, draft: true },
    { ...valid, prerelease: true },
    { ...valid, assets: [] },
    { ...valid, assets: undefined },
    { ...valid, assets: valid.assets.map(item => item.name === 'install.sh' ? { ...item, size: 0 } : item) },
    { ...valid, assets: valid.assets.map(item => item.name === 'SHA256SUMS' ? { ...item, state: 'starter' } : item) },
    { ...valid, assets: valid.assets.map(item => item.name === 'INSTALLER_SHA256SUMS' ? { ...item, size: '9999' } : item) },
  ]) {
    assert.equal(publishedStableTag(invalid), null)
    assert.equal(publishedStableInstall(invalid), null)
  }
  for (const digest of [undefined, null, '', 'sha256:', 'sha256:' + 'A'.repeat(64), 'md5:' + 'a'.repeat(64), 'sha256:' + 'g'.repeat(64)]) {
    const invalid = {
      ...valid,
      assets: valid.assets.map(item => item.name === 'install.sh' ? { ...item, digest } : item),
    }
    assert.equal(publishedStableInstall(invalid), null)
  }
  assert.equal(publishedStableInstall({ tag_name: 'v0.1.0' }), null)
  assert.equal(publishedStableTag({ tag_name: 'v0.1.0' }), 'v0.1.0')
})

test('copied install command is one shell line, exact-tag pinned and checks before sudo', () => {
  assert.equal(stableReleaseApi, 'https://api.github.com/repos/routegate/RouteGate/releases/latest')
  const command = buildInstallCommand('v1.2.3', 'a'.repeat(64))
  assert.ok(command.startsWith('( f=$(mktemp) && trap '))
  assert.ok(!command.includes('\n'))
  assert.match(command, /releases\/download\/v1\.2\.3\/install\.sh/)
  assert.match(command, /sha256sum -c - && sudo bash/)
  assert.match(command, /--version 'v1\.2\.3'/)
  assert.match(command, /curl -q -fLsS --proto '=https' --proto-redir '=https' --tlsv1\.2/)
  assert.ok(!command.includes('nightly.link'))
  assert.ok(!command.includes('raw.githubusercontent.com/routegate/RouteGate/main'))
  assert.ok(!command.includes('curl | sudo bash'))
  assert.ok(!command.includes('less'))
  assert.equal(isSupportedQuickStartTag('v0.1.0'), false)
  assert.equal(isSupportedQuickStartTag('v1.2.3'), true)
  for (const tag of ['v0.1.0', 'main', 'v1.2.3-rc.1', 'v1.2.3; touch /tmp/rg-pwned']) {
    assert.throws(() => buildInstallCommand(tag, 'a'.repeat(64)))
  }
  for (const digest of ['', 'a'.repeat(63), 'g'.repeat(64), 'A'.repeat(64), 'a'.repeat(64) + "'; true"]) {
    assert.throws(() => buildInstallCommand('v1.2.3', digest))
  }
})

test('one copied shell line truly verifies installer, cleans temp file and rejects mismatch before sudo', () => {
  const dir = mkdtempSync(join(tmpdir(), 'routegate-quickstart-'))
  try {
    const bin = join(dir, 'bin')
    mkdirSync(bin)
    const fixture = join(dir, 'installer-source.sh')
    const marker = join(dir, 'sudo-called')
    const capture = join(dir, 'sudo-received-script')
    const source = '#!/usr/bin/env bash\nprintf "reviewed sample\\n"\n'
    writeFileSync(fixture, source)
    const digest = createHash('sha256').update(source).digest('hex')
    const fakeCurl = join(bin, 'curl')
    writeFileSync(fakeCurl, [
      '#!/usr/bin/env bash',
      'set -euo pipefail',
      'dest=""',
      'url=""',
      'while (($#)); do',
      '  case "$1" in',
      '    -o) dest="$2"; shift 2;;',
      '    https:*) url="$1"; shift;;',
      '    *) shift;;',
      '  esac',
      'done',
      '[[ "$url" == "https://github.com/routegate/RouteGate/releases/download/v1.2.3/install.sh" ]]',
      'cp "$ROUTEGATE_TEST_SOURCE" "$dest"',
    ].join('\n') + '\n')
    const fakeSudo = join(bin, 'sudo')
    writeFileSync(fakeSudo, [
      '#!/usr/bin/env bash',
      'set -euo pipefail',
      'printf "%s\\n" "$@" > "$ROUTEGATE_TEST_MARKER"',
      'cp "$2" "$ROUTEGATE_TEST_CAPTURE"',
    ].join('\n') + '\n')
    chmodSync(fakeCurl, 0o755)
    chmodSync(fakeSudo, 0o755)
    const run = expectedHash => spawnSync('bash', ['-c', buildInstallCommand('v1.2.3', expectedHash)], {
      cwd: dir,
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: bin + ':' + process.env.PATH,
        ROUTEGATE_TEST_SOURCE: fixture,
        ROUTEGATE_TEST_MARKER: marker,
        ROUTEGATE_TEST_CAPTURE: capture,
      },
    })
    const success = run(digest)
    assert.equal(success.status, 0, success.stderr)
    assert.match(success.stdout, /: OK/)
    assert.deepEqual(readFileSync(marker, 'utf8').trim().split('\n').filter(Boolean).slice(-2), ['--version', 'v1.2.3'])
    const usedTemp = readFileSync(marker, 'utf8').trim().split('\n')[1]
    assert.equal(existsSync(usedTemp), false, 'temporary installer must be deleted')
    assert.equal(readFileSync(capture, 'utf8'), source)

    rmSync(marker)
    rmSync(capture)
    const mismatch = run('b'.repeat(64))
    assert.notEqual(mismatch.status, 0, 'digest mismatch must fail')
    assert.equal(existsSync(marker), false, 'no privileged command after digest mismatch')
    assert.equal(existsSync(capture), false)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('site does not quietly fall back to a hardcoded obsolete tag', () => {
  const app = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8')
  assert.ok(!app.includes('VERSION=v0.1.0'))
  const readme = readFileSync(new URL('../../README.md', import.meta.url), 'utf8')
  assert.ok(!readme.includes('VERSION=v0.1.0'))
  assert.ok(readme.includes('copy the single Quick Start command'))
  const prerender = readFileSync(new URL('./prerender.mjs', import.meta.url), 'utf8')
  assert.ok(!prerender.includes('Stable v0.1.0 supports'))
  assert.ok(!prerender.includes('Стабильный v0.1.0'))
  assert.match(app, /disabled=\{!installCommand\}/)
  assert.match(app, /status: 'unavailable'/)
  assert.match(app, /status: 'legacy'/)
  assert.match(app, /publishedStableTag\(payload\)/)
  assert.match(app, /publishedStableInstall\(payload\)/)
  assert.match(app, /installerSha256: install\.installerSha256/)
  const styles = readFileSync(new URL('../src/site-refresh.css', import.meta.url), 'utf8')
  assert.match(styles, /white-space: pre-wrap/)
  assert.match(styles, /overflow-wrap: anywhere/)
})
