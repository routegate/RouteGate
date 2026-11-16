// Negative-control tests: prove that documentation CI actually fails on
// broken relative links and retired new-user install commands. Tests mutate
// only the temporary GitHub Actions checkout, always restoring README bytes.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const readme = resolve(root, 'README.md')
const original = readFileSync(readme)

function runCheck() {
  return spawnSync(process.execPath, ['scripts/check-documentation.mjs'], {
    cwd: root,
    encoding: 'utf8',
    timeout: 30000,
  })
}

test('auditor rejects broken relative references and historical first-install commands', () => {
  const clean = runCheck()
  assert.equal(clean.status, 0, clean.stderr || clean.error?.message)

  try {
    writeFileSync(readme, Buffer.concat([
      original,
      Buffer.from('\n[negative-control](docs/this-file-must-not-exist-routegate-2026.md)\n'),
    ]))
    const broken = runCheck()
    assert.notEqual(broken.status, 0, 'broken tracked link must block docs')
    assert.match(broken.stderr, /\[broken-relative-link\]/)
    assert.match(broken.stderr, /README\.md/)
    assert.match(broken.stderr, /this-file-must-not-exist-routegate-2026/)

    writeFileSync(readme, Buffer.concat([original, Buffer.from('\nVERSION=v0.1.0\n')]))
    const obsolete = runCheck()
    assert.notEqual(obsolete.status, 0, 'old executable install recipe must block docs')
    assert.match(obsolete.stderr, /\[obsolete-first-install\]/)
    assert.match(obsolete.stderr, /README\.md/)
  } finally {
    // Always restore even if a subprocess exits, times out, or the assertion fails.
    writeFileSync(readme, original)
  }

  const restored = runCheck()
  assert.equal(restored.status, 0, restored.stderr || restored.error?.message)
  assert.deepEqual(readFileSync(readme), original, 'README bytes must be unchanged')
})
