// RG-80 infrastructure health visual contract. The real widget markup and
// styling remain small and standalone; no production Manager/API is contacted.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, test } from 'node:test';
import { chromium } from 'playwright';

const read = relative => readFileSync(new URL(relative, import.meta.url), 'utf8');
const component = read('../src/pages/dashboard/DashboardPage.tsx');
const css = [
  read('../src/shared/rg80-dashboard.css'),
  read('../src/shared/rg-status-glass.css'),
  read('../src/shared/rg-dashboard-live.css'),
].join('\n');
let browser;

before(async () => {
  browser = await chromium.launch({
    headless: true,
    ...(process.env.ROUTEGATE_CHROMIUM ? { executablePath: process.env.ROUTEGATE_CHROMIUM } : {}),
  });
});
after(async () => { await browser?.close(); });

test('real API values and status categories are preserved in the redesigned widget', () => {
  assert.match(component, /managerHealthy \? 'healthy' : 'warning'/);
  assert.match(component, /serversAvailable \? \(serversCount > 0 \? 'healthy' : 'pending'\) : 'warning'/);
  assert.match(component, /agentsAvailable \? \(agentsCount > 0 \? 'healthy' : 'pending'\) : 'warning'/);
  assert.match(component, /<HealthMetricIcon kind=\{row.kind\}/);
  assert.match(component, /<StatusBadge status=\{row.status\}/);
  assert.match(component, /Decorative status flourish, not a measured traffic or uptime chart/);
});

const variants = [
  { state: 'healthy', label: 'Исправно', value: 'Manager в сети' },
  { state: 'warning', label: 'Проблема', value: '—' },
  { state: 'pending', label: 'Ожидание', value: '0' },
];

function fixture(variant, width) {
  const rows = [
    ['Manager API', variant.value],
    ['Серверы', '3'],
    ['Подключения серверов', '3'],
  ].map(([label, value]) => `
    <div class="health-row health-row--${variant.state}">
      <span class="health-row-icon"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 5h18v14H3z"/></svg></span>
      <div class="health-row-details"><span class="health-row-label">${label}</span><strong class="health-row-value">${value}</strong></div>
      <span class="badge badge-${variant.state}">${variant.label}</span>
    </div>`).join('');
  return `<style>:root {
    --rg-text-primary: #f1f5f9;
    --rg-text-secondary: #a3b2c7;
    --rg-success: #16a34a;
    --rg-live-green: #22c55e;
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: #07111f; color: #f1f5f9; font-family: Arial, sans-serif; }
  .dashboard-page { width: ${width}px; margin: 0 auto; }
  </style><style>${css}</style>
  <div class="dashboard-page">
    <section class="dashboard-widget health-widget">
      <div class="dashboard-widget-header"><h2>Состояние инфраструктуры</h2></div>
      <div class="health-list">${rows}</div>
      <div class="health-pulse health-pulse--${variant.state}" aria-hidden="true">
        <svg viewBox="0 0 240 48" preserveAspectRatio="none">
          <path class="health-pulse-grid" d="M0 24 H240"/>
          <path class="health-pulse-trace" d="M0 24 H70 L78 14 L90 34 L100 24 H240"/>
          <path class="health-pulse-line" d="M0 24 H240"/>
        </svg>
      </div>
    </section>
  </div>`;
}

for (const width of [266, 316, 350, 440]) {
  test(`infrastructure health fits a ${width}px card with spaced rows`, async () => {
    const page = await browser.newPage({ viewport: { width: Math.max(390, width + 20), height: 720 } });
    try {
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      await page.setContent(fixture(variants[0], width));
      const layout = await page.evaluate(() => {
        const panel = document.querySelector('.health-widget');
        const rows = [...document.querySelectorAll('.health-row')];
        const bounds = el => el.getBoundingClientRect();
        const line = document.querySelector('.health-pulse');
        const panelRect = bounds(panel);
        return {
          cardOverflow: panel.scrollWidth > panel.clientWidth + 1,
          contentOverflow: rows.some(row => row.scrollWidth > row.clientWidth + 1),
          rowHeights: rows.map(row => bounds(row).height),
          rowGaps: rows.slice(1).map((row, i) => bounds(row).top - bounds(rows[i]).bottom),
          labelOverlapsBadge: rows.some(row => bounds(row.querySelector('.health-row-details')).right > bounds(row.querySelector('.badge')).left + 1),
          iconSizes: rows.map(row => bounds(row.querySelector('.health-row-icon')).width),
          pulseHeight: bounds(line).height,
          pulseWithinCard: bounds(line).right <= panelRect.right + 1,
          lineAnimation: getComputedStyle(document.querySelector('.health-pulse-line')).animationName,
        };
      });
      assert.equal(layout.cardOverflow, false, 'card must not overflow horizontally');
      assert.equal(layout.contentOverflow, false, 'rows must not overflow horizontally');
      assert.equal(layout.labelOverlapsBadge, false, 'status badge must never cover a label');
      assert.ok(layout.rowHeights.every(height => height >= 51), 'row surfaces require generous height');
      assert.ok(layout.rowGaps.every(gap => gap >= 8), 'row surfaces should remain distinct');
      assert.ok(layout.iconSizes.every(value => value >= 30), 'icons remain visible');
      assert.ok(layout.pulseHeight >= 70 && layout.pulseWithinCard, 'heartbeat stays in its own inset surface');
      assert.equal(layout.lineAnimation, 'rg-heartbeat');
      await page.emulateMedia({ reducedMotion: 'reduce' });
      assert.equal(await page.locator('.health-pulse-line').evaluate(el => getComputedStyle(el).animationName), 'none');
    } finally { await page.close(); }
  });
}

test('unknown and warning states are not styled as falsely healthy', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 680 } });
  try {
    for (const state of variants.slice(1)) {
      await page.setContent(fixture(state, 316));
      const actual = await page.evaluate(() => ({
        badges: [...document.querySelectorAll('.health-row .badge')].map(el => el.className),
        pulse: document.querySelector('.health-pulse').className,
      }));
      assert.ok(actual.badges.every(value => !value.includes('badge-healthy')));
      assert.ok(actual.badges.every(value => value.includes('badge-' + state.state)));
      assert.ok(actual.pulse.includes('health-pulse--' + state.state));
    }
  } finally { await page.close(); }
});
