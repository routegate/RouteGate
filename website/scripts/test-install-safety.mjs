import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readFileSync } from 'node:fs'

const app = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8')
const content = readFileSync(new URL('../src/content.ts', import.meta.url), 'utf8')
const prerender = readFileSync(new URL('./prerender.mjs', import.meta.url), 'utf8')

test('public site cannot copy or display an obsolete hardcoded installer', () => {
  assert.doesNotMatch(app, /VERSION=v0\.1\.0/)
  assert.doesNotMatch(app, /raw\.githubusercontent\.com\/routegate\/RouteGate\/\$\{VERSION\}\/install\.sh/)
  assert.doesNotMatch(app, /installCommand|handleCopyInstall/)
  assert.match(app, /<button type="button" disabled aria-disabled="true">/)
  assert.match(app, /className="install-unavailable" role="status"/)
  assert.match(app, /issues\/715/)
})

test('both languages clearly block current installation until release acceptance', () => {
  assert.match(content, /commandTitle: 'Установщик v0\.2\.0 готовится к выпуску'/)
  assert.match(content, /commandTitle: 'RouteGate v0\.2\.0 installer is being prepared'/)
  assert.match(content, /commandButton: 'Недоступно'/)
  assert.match(content, /commandButton: 'Unavailable'/)
  assert.match(content, /commandStatusLink: 'Статус подготовки релиза'/)
  assert.match(content, /commandStatusLink: 'Release readiness status'/)
  assert.match(content, /Исторический релиз v0\.1\.0/)
  assert.match(content, /Historical release v0\.1\.0/)
  assert.match(prerender, /Historical release v0\.1\.0/)
})
