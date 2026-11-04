// Chromium contract for the screenshot-reported Dashboard online indicator mismatch.
// Use the same ordered CSS rules as the app, without touching the production API.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { readFileSync } from 'node:fs';
import { chromium } from 'playwright';

const read = relative => readFileSync(new URL(relative, import.meta.url), 'utf8');
const css = [
  read('../src/shared/rg80-dashboard-tables.css'),
  read('../src/pages/dashboard/DashboardPage.css'),
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

for (const width of [390, 1440, 1680]) {
  test(`ambient animation keeps scroll bounds stable at ${width}px`, async () => {
    const page = await browser.newPage({ viewport: { width, height: 900 } });
    try {
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      await page.setContent(`<style>${css}</style>
        <style>body { margin: 0; } main { padding: 16px; }
          .dashboard-page { height: 1200px; }</style>
        <main><div class="dashboard-page"><button>Dashboard action</button></div></main>`);
      const sample = () => page.evaluate(() => {
        const dashboard = document.querySelector('.dashboard-page');
        const animation = dashboard.getAnimations({ subtree: true }).find(a => a.animationName === 'rg-aurora');
        if (!animation) throw new Error('Expected ambient animation');
        animation.pause();
        return [0, 4500, 9000, 13500, 17999].map(time => {
          animation.currentTime = time;
          const rect = dashboard.getBoundingClientRect();
          const style = getComputedStyle(dashboard, '::before');
          return {
            width: document.documentElement.scrollWidth,
            height: document.documentElement.scrollHeight,
            viewport: document.documentElement.clientWidth,
            x: rect.x, y: rect.y, panelWidth: rect.width, panelHeight: rect.height,
            opacity: style.opacity,
          };
        });
      });
      const frames = await sample();
      const { opacity: _, ...expected } = frames[0];
      for (const { opacity, ...frame } of frames) {
        assert.deepEqual(frame, expected, 'Animation must not change scroll bounds or panel geometry');
        assert.ok(frame.width <= frame.viewport, 'No horizontal document overflow');
        assert.equal(frame.height, 1232, 'No extra vertical scroll area from the background');
      }
      assert.notEqual(frames[0].opacity, frames[2].opacity, 'Ambient feedback remains animated');
      await page.getByRole('button', { name: 'Dashboard action' }).focus();
      assert.equal(await page.getByRole('button').evaluate(el => document.activeElement === el), true);

      // Negative control: the shipped oversized/translated background reproduces
      // the screenshot's moving scrollbar even with entirely static content.
      const old = await page.addStyleTag({ content: `
        .dashboard-page::before { inset: -40px; }
        @keyframes rg-aurora {
          0%, 100% { transform: translate3d(0, 0, 0) scale(1); }
          50% { transform: translate3d(4%, 3%, 0) scale(1.08); }
        }` });
      const broken = await sample();
      assert.ok(broken.some(frame => frame.width > frame.viewport));
      assert.ok(new Set(broken.map(frame => `${frame.width}/${frame.height}`)).size > 1,
        'Old background must reproduce moving document bounds');
      await old.evaluate(el => el.remove());
      await page.emulateMedia({ reducedMotion: 'reduce' });
      assert.equal(await page.locator('.dashboard-page').evaluate(el =>
        getComputedStyle(el, '::before').animationName), 'none');
    } finally {
      await page.close();
    }
  });
}

for (const width of [390, 1440]) {
  test('server and VPN online dots render and pulse identically at ' + width + 'px', async () => {
    const page = await browser.newPage({ viewport: { width, height: 900 } });
    try {
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      const html = [
        '<div class="dashboard-page">',
        '<section class="health-pulse health-pulse--healthy">',
        '<svg viewBox="0 0 240 48"><path class="health-pulse-line" d="M0 24 H240"></path></svg>',
        '</section><div class="node-distribution-map"></div>',
        '<section class="traffic-empty-state"><strong>Traffic unavailable</strong></section>',
        '<div class="dashboard-table servers-summary-table">',
        '<div class="dashboard-table-row"><span class="server-online-dot"></span></div>',
        '<div class="dashboard-table-row"><span class="server-offline-dot"></span></div>',
        '</div>',
        '<div class="online-users-table dashboard-table">',
        '<div class="dashboard-table-row"><span class="online-user-state online-user-state--online"><i></i>Online</span></div>',
        '<div class="dashboard-table-row"><span class="online-user-state online-user-state--recent"><i></i>Recently active</span></div>',
        '</div></div>',
      ].join('');
      await page.setContent('<style>:root { --rg-success: #22c55e; }</style><style>' + css + '</style>' + html);
      const result = await page.evaluate(() => {
        const select = s => document.querySelector(s);
        const server = select('.server-online-dot');
        const online = select('.online-user-state--online i');
        const recent = select('.online-user-state--recent i');
        const offline = select('.server-offline-dot');
        const serverAnimation = server.getAnimations().find(a => a.animationName === 'rg-status-dot-breathe');
        const userAnimation = online.getAnimations().find(a => a.animationName === 'rg-status-dot-breathe');
        const style = element => getComputedStyle(element);
        const base = {
          server: {
            width: style(server).width,
            height: style(server).height,
            background: style(server).backgroundColor,
            duration: style(server).animationDuration,
            timing: style(server).animationTimingFunction,
            name: style(server).animationName,
          },
          user: {
            width: style(online).width,
            height: style(online).height,
            background: style(online).backgroundColor,
            duration: style(online).animationDuration,
            timing: style(online).animationTimingFunction,
            name: style(online).animationName,
          },
          parentOverflow: style(select('.online-user-state--online')).overflow,
          recentAnimation: style(recent).animationName,
          offlineAnimation: style(offline).animationName,
          heartbeatDuration: style(select('.health-pulse-line')).animationDuration,
          radarDuration: getComputedStyle(select('.node-distribution-map'), '::after').animationDuration,
        };
        if (!serverAnimation || !userAnimation) return { base, missingAnimation: true };
        serverAnimation.pause();
        userAnimation.pause();
        function state(time) {
          serverAnimation.currentTime = time;
          userAnimation.currentTime = time;
          const get = dot => ({
            opacity: Number(style(dot).opacity),
            transform: style(dot).transform,
            glow: style(dot).boxShadow,
          });
          return { server: get(server), user: get(online) };
        }
        return { base, full: state(0), dim: state(2600) };
      });
      assert.equal(result.missingAnimation, undefined, 'Both statuses need the same running animation');
      assert.deepEqual(result.base.server, result.base.user, 'Online dot visual design must match across tables');
      assert.equal(result.base.server.width, '9px');
      assert.equal(result.base.server.height, '9px');
      assert.equal(result.base.server.duration, '5.2s');
      assert.equal(result.base.parentOverflow, 'visible', 'Online state must not clip dot glow');
      assert.equal(result.base.recentAnimation, 'none');
      assert.equal(result.base.offlineAnimation, 'none');
      assert.equal(result.base.heartbeatDuration, '6s');
      assert.equal(result.base.radarDuration, '14s');
      assert.deepEqual(result.full.server, result.full.user, 'At full brightness the dots are identical');
      assert.deepEqual(result.dim.server, result.dim.user, 'At half cycle the dots are identical');
      assert.ok(result.full.server.opacity >= 0.99);
      assert.ok(result.dim.server.opacity >= 0.65 && result.dim.server.opacity < result.full.server.opacity - 0.2);

      await page.emulateMedia({ reducedMotion: 'reduce' });
      const reduced = await page.evaluate(() => {
        const style = s => getComputedStyle(document.querySelector(s)).animationName;
        return {
          server: style('.server-online-dot'),
          online: style('.online-user-state--online i'),
          heartbeat: style('.health-pulse-line'),
          radar: getComputedStyle(document.querySelector('.node-distribution-map'), '::after').animationName,
        };
      });
      assert.deepEqual(reduced, { server: 'none', online: 'none', heartbeat: 'none', radar: 'none' });
    } finally {
      await page.close();
    }
  });
}
