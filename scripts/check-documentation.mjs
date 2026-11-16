#!/usr/bin/env node
// Offline, deterministic audit of ALL tracked Markdown/text docs.
// --report displays issues without failing, for triage of historical material.
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { dirname, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const report = process.argv.includes('--report')
const tracked = execFileSync('git', ['-C', root, 'ls-files', '-z'], { encoding: 'utf8' }).split('\0').filter(Boolean)
const trackedSet = new Set(tracked)
const docs = tracked.filter(p => /\.(?:md|mdx|rst|txt)$/i.test(p))
const findings = []
let internalCount = 0, externalCount = 0
const flag = (file, line, category, target) => findings.push({ file, line, category, target })
const lineOf = (body, index) => body.slice(0, index).split('\n').length

function existsInRepo(path) {
  const abs = resolve(root, path)
  if (abs !== root && !abs.startsWith(root + sep)) return false
  if (!existsSync(abs)) return false
  try {
    if (statSync(abs).isDirectory()) {
      const p = relative(root, abs).split(sep).join('/')
      return tracked.some(item => item.startsWith(p + '/'))
    }
    return trackedSet.has(relative(root, abs).split(sep).join('/'))
  } catch { return false }
}

function ownedGithubPath(value) {
  try {
    const u = new URL(value)
    if (u.hostname === 'github.com') {
      return u.pathname.match(/^\/routegate\/RouteGate\/(?:blob|tree)\/main\/(.+)$/i)?.[1] ?? null
    }
    if (u.hostname === 'raw.githubusercontent.com') {
      return u.pathname.match(/^\/routegate\/RouteGate\/main\/(.+)$/i)?.[1] ?? null
    }
  } catch { /* malformed URL reported separately by link check */ }
  return null
}

function checkLink(file, line, value) {
  let target = value.trim().replace(/^<|>$/g, '')
  target = target.replace(/\s+(?:"[^"]*"|'[^']*')\s*$/, '')
  if (!target || /^(?:#|mailto:|tel:|data:|javascript:)/i.test(target)) return
  if (target.includes('$' + '{') || /[<>]/.test(target)) return
  if (/^(?:https?:|\/\/)/i.test(target)) {
    externalCount++
    const own = ownedGithubPath(target)
    if (own) {
      try {
        if (!existsInRepo(decodeURIComponent(own))) flag(file, line, 'broken-repository-url', target)
      } catch { flag(file, line, 'invalid-repository-url', target) }
    }
    return
  }
  if (/^[A-Za-z][A-Za-z0-9+.-]*:/.test(target)) return
  // Absolute /api, /ru and website paths are not repository-relative.
  if (target.startsWith('/')) return
  internalCount++
  try {
    const part = decodeURIComponent(target.split('#')[0].split('?')[0]).replace(/\\([()])/g, '$1')
    if (part && !existsInRepo(relative(root, resolve(root, dirname(file), part)))) {
      flag(file, line, 'broken-relative-link', target)
    }
  } catch { flag(file, line, 'invalid-relative-link', target) }
}

function scan(file, body) {
  let fenced = false
  const lines = body.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const ln = lines[i]
    if (/^\s{0,3}(?:\x60{3,}|~{3,})/.test(ln)) { fenced = !fenced; continue }
    if (fenced) continue
    for (const match of ln.matchAll(/!?\[[^\]]+\]\((<[^>]+>|[^)\n]+)\)/g)) checkLink(file, i + 1, match[1])
    const ref = ln.match(/^\s{0,3}\[[^\]]+\]:\s*(<[^>]+>|\S+)/)
    if (ref) checkLink(file, i + 1, ref[1])
    for (const match of ln.matchAll(/\b(?:href|src)=["']([^"']+)["']/g)) checkLink(file, i + 1, match[1])
  }
}

for (const file of docs) scan(file, readFileSync(resolve(root, file), 'utf8'))

// Only actively recommended new-host docs are subject to the current install
// contract. Historical release notes and acceptance reports must remain intact.
for (const file of ['README.md', 'docs/guides/first-install.md', 'docs/deployment/clean-vps-installer.md', 'docs/observability/grafana.md', 'docs/deployment/mvp-deployment-baseline.md']) {
  const body = readFileSync(resolve(root, file), 'utf8')
  if (!body.includes('https://routegate.org/#install')) flag(file, 1, 'missing-current-Quick-Start', 'https://routegate.org/#install')
  for (const pattern of [
    /VERSION\s*=\s*v0\.1\.0\b/g,
    /--version\s+v0\.1\.0\b/g,
    /sudo\s+bash\s+routegate-install(?:-v0\.1\.0)?\.sh\b/g,
    /raw\.githubusercontent\.com\/routegate\/RouteGate\/v0\.1\.0\/install\.sh/g,
  ]) {
    for (const m of body.matchAll(pattern)) flag(file, lineOf(body, m.index), 'obsolete-first-install', m[0])
  }
}

// Optional full-source inventory: Markdown links alone cannot see install URLs
// inside shell code blocks or inline code. Print every suspicious reference in
// every documentation file for human review without breaking historical notes.
if (process.argv.includes('--inventory')) {
  const references = []
  const checks = [
    ['legacy-install-tag', /(?:VERSION\s*=\s*v0\.1\.0\b|--version\s+v0\.1\.0\b|\/v0\.1\.0\/install\.sh\b)/],
    ['unpublished-version-url', /\/releases\/download\/v0\.2\.0(?:-[^/\s]+)?\//],
    ['legacy-github-owner', /(?:github\.com|raw\.githubusercontent\.com)\/ikaevus\/RouteGate/],
    ['old-pages-host', /routegate\.github\.io\/RouteGate/],
    ['moving-branch-installer', /(?:githubusercontent\.com\/routegate\/RouteGate\/(?:main|HEAD)\/install\.sh|raw\.githubusercontent\.com\/routegate\/RouteGate\/(?:main|HEAD)\/install\.sh)/],
    ['explicit-install-script', /(?:curl|wget|bash|sudo).{0,130}(?:install\.sh|routegate-install)/],
  ]
  for (const file of docs) {
    for (const [index, line] of readFileSync(resolve(root, file), 'utf8').split('\n').entries()) {
      for (const [type, re] of checks) {
        if (re.test(line)) references.push({ file, line: index + 1, type, text: line.trim().slice(0, 185) })
      }
    }
  }
  const counts = {}
  for (const ref of references) counts[ref.type] = (counts[ref.type] ?? 0) + 1
  console.log('[docs-inventory] source-level install references by type: ' + JSON.stringify(counts))
  for (const ref of references.slice(0, 175)) {
    console.log('[docs-inventory] ' + ref.file + ':' + ref.line + ' [' + ref.type + '] ' + ref.text)
  }
  if (references.length > 175) console.log('[docs-inventory] +' + (references.length - 175) + ' further references')
}

findings.sort((a, b) => a.file.localeCompare(b.file) || a.line - b.line)
for (const f of findings) console.error('[docs] ' + f.file + ':' + f.line + ' [' + f.category + '] ' + f.target)
console.log('[docs] scanned ' + docs.length + ' documents; ' + internalCount + ' local links; ' +
  externalCount + ' external links (offline audit; external HTTP not verified)')
console.log('[docs] findings: ' + findings.length + (report ? ' (report only)' : ' (blocking)'))
if (findings.length && !report) process.exitCode = 1
