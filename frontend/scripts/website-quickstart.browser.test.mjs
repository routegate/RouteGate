// Real Chromium acceptance of the public website's copy/paste entrypoint.
// Runs entirely on a temporary local Vite server, with GitHub release metadata
// mocked and with NO server installation, release publication, or network API use.
//
// This verifies the ordinary visitor's interaction, not just string construction:
// disabled legacy, verified stable, actual clipboard, and rejected incomplete
// release assets, in RU and EN.
import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'

const websiteRoot = fileURLToPath(new URL('../../website/', import.meta.url))
const origin = 'http://127.0.0.1:15176'
const digest = 'a'.repeat(64)
const release = (tag = 'v0.2.0') => ({
  tag_name: tag,
  draft: false,
  prerelease: false,
  assets: [
    'install.sh', 'SHA256SUMS', 'INSTALLER_SHA256SUMS',
    'release-manifest.json', 'routegate-' + tag + '-linux-amd64.tar.gz',
  ].map(name => ({
    name,
    size: 1024,
    state: 'uploaded',
    ...(name === 'install.sh' ? { digest: 'sha256:' + digest } : {}),
  })),
})

let vite
let browser
let output = ''

before(async () => {
  vite = spawn('npm', ['run', 'dev', '--', '--host', '127.0.0.1', '--port', '15176', '--strictPort'], {
    cwd: websiteRoot,
    env: { ...process.env, BROWSER: 'none' },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  for (const stream of [vite.stdout, vite.stderr]) stream.on('data', chunk => {
    output = (output + chunk.toString()).slice(-5000)
  })

  let ready = false
  for (let attempt = 0; attempt < 80; attempt++) {
    if (vite.exitCode !== null) break
    try {
      const res = await fetch(origin + '/')
      if (res.ok) { ready = true; break }
    } catch { /* Vite has not started */ }
    await new Promise(resolve => setTimeout(resolve, 250))
  }
  assert.equal(ready, true, 'website Vite startup failed: ' + output)
  browser = await chromium.launch({
    headless: true,
    ...(process.env.ROUTEGATE_CHROMIUM ? { executablePath: process.env.ROUTEGATE_CHROMIUM } : {}),
  })
})

after(async () => {
  await browser?.close()
  if (vite && vite.exitCode === null) vite.kill('SIGTERM')
})

async function openSite(path, releasePayload) {
  const context = await browser.newContext({
    viewport: { width: path.startsWith('/ru') ? 390 : 1440, height: 900 },
    permissions: ['clipboard-read', 'clipboard-write'],
  })
  const page = await context.newPage()
  await page.route('**/quickstart-release.json', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(releasePayload) }))
  // A good same-origin snapshot works even when the GitHub API is blocked.
  await page.route('https://api.github.com/**', route => route.abort())
  await page.goto(origin + path, { waitUntil: 'domcontentloaded' })
  const button = page.locator('.install-panel-heading button')
  await button.waitFor({ state: 'visible' })
  return { context, page, button }
}

for (const [locale, path] of [['ru', '/ru/'], ['en', '/']]) {
  test(locale + ': copied Quick Start is exactly one verified pinned command', async () => {
    const { context, page, button } = await openSite(path, release())
    try {
      await page.waitForFunction(() => {
        const el = document.querySelector('.install-panel-heading button')
        return el && !el.disabled
      })
      const displayed = (await page.locator('.install-panel pre code').innerText()).trim()
      assert.equal(displayed.includes('\n'), false, 'one paste must be one shell line')
      assert.match(displayed, /releases\/download\/v0\.2\.0\/install\.sh/)
      assert.match(displayed, /sha256sum -c -/)
      assert.match(displayed, /--version 'v0\.2\.0'/)
      assert.ok(displayed.includes(digest), 'the release digest must be copied')
      assert.ok(!displayed.includes('v0.1.0'), 'never regress to historic MVP')
      assert.ok(!displayed.includes('curl | sudo bash'), 'no unverified pipe to root')

      await button.click()
      const copied = await page.evaluate(() => navigator.clipboard.readText())
      assert.equal(copied, displayed, 'clipboard must equal the visible command')
      assert.equal(await button.isEnabled(), true)
    } finally {
      await context.close()
    }
  })

  test(locale + ': old MVP and unverified release cannot be copied', async () => {
    for (const invalid of [
      { tag_name: 'v0.1.0' },
      { ...release(), assets: release().assets.map(item =>
        item.name === 'install.sh' ? { ...item, digest: undefined } : item) },
      { ...release(), prerelease: true },
    ]) {
      const { context, page, button } = await openSite(path, invalid)
      try {
        await page.waitForFunction(() => {
          const el = document.querySelector('.install-panel-heading button')
          const heading = document.querySelector('.install-panel-heading strong')?.textContent ?? ''
          return el?.disabled && heading.length > 0 && !/Проверяем|Checking/.test(heading)
        })
        assert.equal(await button.isDisabled(), true)
        const pageText = (await page.locator('.install-panel pre code').innerText()).trim()
        assert.doesNotMatch(pageText, /curl\s+-/)
        assert.doesNotMatch(pageText, /releases\/download\/v0\.1\.0/)
      } finally {
        await context.close()
      }
    }
  })
}
