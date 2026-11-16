// Real Chromium acceptance of the public website's copy/paste entrypoint.
// Runs entirely on a temporary local Vite server, with GitHub release metadata
// mocked and with NO server installation, release publication, or network API use.
//
// This verifies the ordinary visitor's interaction, not just string construction:
// disabled legacy, verified stable, actual clipboard, and rejected incomplete
// release assets, in RU and EN.
import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'
import { createServer } from 'vite'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { requiredPublishedAssets } from '../../website/src/release.ts'

const websiteRoot = fileURLToPath(new URL('../../website/', import.meta.url))
const origin = 'http://127.0.0.1:15176'
const digest = 'a'.repeat(64)
const release = (tag = 'v0.2.0') => ({
  tag_name: tag,
  draft: false,
  prerelease: false,
  assets: requiredPublishedAssets(tag).map(name => ({
    name,
    size: 1024,
    state: 'uploaded',
    ...(name === 'install.sh' ? { digest: 'sha256:' + digest } : {}),
  })),
})

let vite
let browser

before(async () => {
  // Use Vite's JS server lifecycle, not a nested npm child process, so
  // Chromium CI reliably closes all listeners even after an assertion fails.
  vite = await createServer({
    root: websiteRoot,
    logLevel: 'error',
    server: { host: '127.0.0.1', port: 15176, strictPort: true },
  })
  await vite.listen()
  browser = await chromium.launch({
    headless: true,
    ...(process.env.ROUTEGATE_CHROMIUM ? { executablePath: process.env.ROUTEGATE_CHROMIUM } : {}),
  })
})

after(async () => {
  await browser?.close()
  await vite?.close()
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
      const heroCta = page.locator('.hero-actions a[href="#install"]')
      assert.equal(await heroCta.isVisible(), true, 'Quick Start link must be visible in the first screen')
      await heroCta.click()
      assert.equal(new URL(page.url()).hash, '#install', 'hero install link must navigate to the install section')
      assert.equal(await button.isVisible(), true, 'copy button must be present at the install destination')
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
