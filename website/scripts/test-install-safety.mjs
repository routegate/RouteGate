import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'

const app = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8')
const content = readFileSync(new URL('../src/content.ts', import.meta.url), 'utf8')

test('public Quick Start never copies the obsolete v0.1.0 install command', () => {
  assert.doesNotMatch(app, /VERSION=v0\.1\.0/)
  assert.doesNotMatch(app, /raw\.githubusercontent\.com\/routegate\/RouteGate\/\$\{VERSION\}\/install\.sh/)

  if (app.includes('buildInstallCommand')) {
    // Future reviewed Quick Start must not be disabled forever by a hotfix-only test.
    // It may turn on only after complete release metadata verifies the SHA-256 digest.
    assert.match(app, /publishedStableInstall\(payload\)/)
    assert.match(app, /disabled=\{!installCommand\}/)
    assert.match(app, /if \(!installCommand\) return/)
    const release = readFileSync(new URL('../src/release.ts', import.meta.url), 'utf8')
    assert.match(release, /tag !== 'v0\.1\.0'/)
    assert.match(release, /installerSha256/)
    assert.match(release, /sha256sum -c -/)
  } else {
    // Emergency main-only hotfix: block old installer, do not imply v0.2.0 is out.
    assert.match(app, /<button type="button" disabled aria-disabled="true">/)
    assert.match(app, /className="install-unavailable" role="status"/)
    assert.match(app, /issues\/715/)
  }
})

test('public site explains release status in both supported languages', () => {
  assert.match(content, /v0\.1\.0/)
  assert.match(content, /v0\.2\.0/)
  assert.match(content, /БЫСТРЫЙ СТАРТ/)
  assert.match(content, /QUICK START/)
  // The hotfix can use static disabled text, while the future release-aware UI
  // uses runtime loading/legacy/ready/error states. Both must be bilingual.
  if (app.includes('buildInstallCommand')) {
    assert.match(content, /legacyReleaseDisabled/)
    assert.match(content, /releaseUnavailableNote/)
  } else {
    assert.match(content, /commandButton: 'Недоступно'/)
    assert.match(content, /commandButton: 'Unavailable'/)
  }
})

test('installation is prominent and honest on desktop and mobile', () => {
  const installPosition = app.indexOf('className="section deployment-section container" id="install"')
  const productPosition = app.indexOf('className="section product-section container" id="product"')
  assert.ok(installPosition !== -1 && installPosition < productPosition,
    'Quick Start must follow hero, not hide after product tour')
  assert.match(app, /className="nav-install-link" href="#install"/)
  assert.match(app, /className="hero-install-status"/)
  assert.match(content, /start: 'Установка RouteGate'/)
  assert.match(content, /start: 'Install RouteGate'/)
  assert.match(content, /installStatus: 'v0\.2\.0 готовится к выпуску/)
  assert.match(content, /installStatus: 'v0\.2\.0 is under review/)
})
