import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

// CI checks out the whole monorepo. The Docker/dev frontend can build from its
// own directory, so a byte-for-byte mirror lives inside src/shared.
const root = fileURLToPath(new URL('../../', import.meta.url));
const read = (path) => readFileSync(join(root, path), 'utf8');

const brandCss = read('brand/tokens/routegate-tokens.css');
const appCss = read('frontend/src/shared/brand-tokens.css');
const brandJson = JSON.parse(read('brand/tokens/routegate-tokens.json'));
const entrypoint = read('frontend/src/main.tsx');
const uiCss = read('frontend/src/shared/rg80.css');

test('standalone frontend uses an exact copy of the official /brand CSS tokens', () => {
  assert.equal(appCss, brandCss, 'Copy /brand/tokens/routegate-tokens.css to frontend/src/shared/brand-tokens.css');
});

test('official CSS token values remain aligned with the JSON brand palette', () => {
  const colorNames = {
    background: 'bg',
    surface: 'surface',
    surface_2: 'surface-2',
    line: 'line',
    primary_blue: 'primary',
    accent_blue: 'primary-accent',
    text: 'text',
    muted: 'muted',
    success: 'success',
    warning: 'warning',
    danger: 'danger',
  };
  for (const [jsonName, cssName] of Object.entries(colorNames)) {
    const declaration = new RegExp(`--rg-color-${cssName}:\\s*${brandJson.color[jsonName]};`, 'i');
    assert.match(brandCss, declaration, `Brand token mismatch: ${jsonName}`);
  }
  for (const [name, value] of Object.entries(brandJson.radius)) {
    assert.match(brandCss, new RegExp(`--rg-radius-${name}:\\s*${value};`));
  }
  assert.match(brandCss, /--rg-font-sans:\s*Inter,/);
});

test('token stylesheet precedes all legacy UI layers', () => {
  const tokenImport = entrypoint.indexOf("import './shared/brand-tokens.css';");
  const firstLegacy = entrypoint.indexOf("import './shared/styles.css';");
  const brandOverride = entrypoint.indexOf("import './shared/rg-brand.css';");
  assert.ok(tokenImport >= 0 && firstLegacy > tokenImport && brandOverride > firstLegacy);
});

test('RG-80 identity aliases reference the official design tokens', () => {
  const aliases = {
    'bg-deep': 'bg',
    'bg-main': 'surface',
    primary: 'primary',
    success: 'success',
    warning: 'warning',
    danger: 'danger',
    'text-primary': 'text',
    'text-secondary': 'muted',
  };
  for (const [alias, name] of Object.entries(aliases)) {
    assert.match(uiCss, new RegExp(`--rg-${alias}:\\s*var\\(--rg-color-${name}\\);`));
  }
  assert.doesNotMatch(uiCss, /\.brand-mark\s*\{\s*background:\s*linear-gradient/);
});
