#!/usr/bin/env node
// Online validation of exact, pinned installer and release-download URLs in
// tracked project documentation. Does not rewrite anything or touch servers.
// Uses GitHub's authenticated API to avoid unreliable raw/CDN HEAD redirects.
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const files = execFileSync('git', ['-C', root, 'ls-files', '-z'], { encoding: 'utf8' })
  .split('\0').filter(path => /\.(?:md|mdx|rst|txt)$/i.test(path))
const token = process.env.GITHUB_TOKEN ?? process.env.GH_TOKEN ?? ''
const headers = {
  'Accept': 'application/vnd.github+json',
  'X-GitHub-Api-Version': '2022-11-28',
  'User-Agent': 'RouteGate-documentation-link-check',
  ...(token ? { Authorization: 'Bearer ' + token } : {}),
}
const targets = new Map()
const errors = []

for (const file of files) {
  const source = readFileSync(resolve(root, file), 'utf8')
  const lines = source.split('\n')
  for (const [i, line] of lines.entries()) {
    const patterns = [
      ['tag-file', /https:\/\/raw\.githubusercontent\.com\/routegate\/RouteGate\/(v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?)\/([A-Za-z0-9._/-]+)/g],
      ['release-asset', /https:\/\/github\.com\/routegate\/RouteGate\/releases\/download\/(v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?)\/([A-Za-z0-9._/-]+)/g],
    ]
    for (const [kind, re] of patterns) {
      for (const match of line.matchAll(re)) {
        const [_, tag, name] = match
        const key = [kind, tag, name].join('|')
        const uses = targets.get(key) ?? []
        uses.push({ file, line: i + 1 })
        targets.set(key, uses)
      }
    }
  }
}

async function queryJson(url) {
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const response = await fetch(url, { headers, signal: AbortSignal.timeout(15000) })
      if ((response.status === 429 || response.status >= 500) && attempt < 2) continue
      if (!response.ok) return { status: response.status }
      return { status: response.status, body: await response.json() }
    } catch (e) {
      if (attempt === 2) throw e
    }
  }
  throw new Error('GitHub API retries exhausted')
}

for (const [key, uses] of targets) {
  const [kind, tag, filename] = key.split('|')
  let response
  try {
    if (kind === 'tag-file') {
      response = await queryJson('https://api.github.com/repos/routegate/RouteGate/contents/' +
        filename.split('/').map(encodeURIComponent).join('/') + '?ref=' + encodeURIComponent(tag))
      if (response.status !== 200 || response.body?.type !== 'file') {
        errors.push({ key, uses, status: response.status })
      }
    } else {
      response = await queryJson('https://api.github.com/repos/routegate/RouteGate/releases/tags/' +
        encodeURIComponent(tag))
      if (response.status !== 200 ||
          !Array.isArray(response.body?.assets) ||
          !response.body.assets.some(a => a.name === filename && a.state === 'uploaded' && a.size > 0)) {
        errors.push({ key, uses, status: response.status })
      }
    }
  } catch (e) {
    errors.push({ key, uses, error: String(e) })
  }
}

for (const e of errors) {
  console.error('[release-doc-link] BROKEN ' + e.key + ' status=' + (e.status ?? e.error) +
    ' referenced at ' + e.uses.map(x => x.file + ':' + x.line).join(', '))
}
console.log('[release-doc-link] checked ' + targets.size +
  ' exact pinned GitHub release/tag URLs across ' + files.length + ' documents; broken=' + errors.length)
if (errors.length) process.exitCode = 1
