#!/usr/bin/env node
// Before building a tagged release, prove that GitHub has actively enforced
// tag rules preventing BOTH ref updates and deletions for the intended tag.
// This read-only verifier does not create rulesets or publish a release.
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

export function tagRulesCoverRelease(rulesets, tag) {
  if (!/^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)(?:-rc\.(?:0|[1-9]\d*))?$/.test(tag)) {
    return { protected: false, reason: 'not a supported RouteGate release tag' }
  }
  if (!Array.isArray(rulesets)) return { protected: false, reason: 'missing ruleset data' }

  const active = rulesets.filter(item => {
    if (!item || item.target !== 'tag' || item.enforcement !== 'active') return false
    const refs = item.conditions?.ref_name
    if (!Array.isArray(refs?.include) || !refs.include.some(ref =>
      ref === 'refs/tags/v*' || ref === '~ALL'
    )) return false
    // Strict release policy: target all v* tags without any exclusions. This
    // may reject some valid but unusual policies; false negatives are safer
    // than incorrectly allowing mutable published installer source.
    return Array.isArray(refs.exclude) && refs.exclude.length === 0
  })
  const types = new Set(active.flatMap(item =>
    Array.isArray(item.rules) ? item.rules.map(rule => rule?.type) : []
  ))
  const missing = ['update', 'deletion'].filter(type => !types.has(type))
  return {
    protected: missing.length === 0,
    matchingRulesets: active.length,
    reason: missing.length ? 'missing active ' + missing.join(' and ') + ' restriction' : 'active tag protection present',
  }
}

async function apiJson(path, token) {
  const response = await fetch('https://api.github.com/' + path, {
    signal: AbortSignal.timeout(15000),
    headers: {
      Accept: 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28',
      'User-Agent': 'RouteGate-tag-release-protection-gate',
      ...(token ? { Authorization: 'Bearer ' + token } : {}),
    },
  })
  if (!response.ok) throw new Error('GitHub rulesets API returned HTTP ' + response.status)
  return response.json()
}

export async function verifyRemoteTagProtection(repo, tag, token) {
  if (!/^[\w.-]+\/[\w.-]+$/.test(repo)) throw new Error('Invalid repository name')
  const list = await apiJson('repos/' + repo + '/rulesets?includes_parents=true&per_page=100', token)
  if (!Array.isArray(list)) throw new Error('GitHub did not return a ruleset list')
  if (list.length >= 100) throw new Error('Ruleset list may be paginated; review required before release')
  const relevant = list.filter(item => item?.target === 'tag' && item.enforcement === 'active')
  // The list response is insufficient: only full ruleset details contain
  // ref include/exclude conditions and the actual rule types.
  const details = await Promise.all(relevant.map(item => {
    if (!Number.isSafeInteger(item.id) || item.id <= 0) throw new Error('Missing active ruleset ID')
    return apiJson('repos/' + repo + '/rulesets/' + item.id + '?includes_parents=true', token)
  }))
  return tagRulesCoverRelease(details, tag)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const tag = process.argv[2] ?? ''
  const repo = process.env.GITHUB_REPOSITORY ?? 'routegate/RouteGate'
  const token = process.env.GH_TOKEN ?? process.env.GITHUB_TOKEN ?? ''
  try {
    const result = await verifyRemoteTagProtection(repo, tag, token)
    if (!result.protected) throw new Error(result.reason)
    console.log('[release] protected tag ruleset verified for ' + repo + ': ' + tag)
    console.log('[release] verify bypass actors separately in GitHub Rules settings')
  } catch (error) {
    console.error('::error::Release tag protection gate: ' + error.message)
    console.error('Create an Active tag ruleset for refs/tags/v* with Restrict updates + Restrict deletions; do not bypass it.')
    process.exitCode = 1
  }
}
