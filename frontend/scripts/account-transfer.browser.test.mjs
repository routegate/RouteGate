// Controlled UI fixtures: verifies the canonical assignment is not changed by
// Prepare, and source cleanup always requires an explicit client check.
import assert from 'node:assert/strict';
import { before, after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';
import { createServer } from 'vite';
let vite, browser;
const origin = 'http://127.0.0.1:15179';
before(async () => {
  vite = await createServer({ root: fileURLToPath(new URL('../', import.meta.url)), logLevel: 'error', server: { host: '127.0.0.1', port: 15179, strictPort: true } });
  await vite.listen();
  browser = await chromium.launch({ headless: true, ...(process.env.ROUTEGATE_CHROMIUM ? { executablePath: process.env.ROUTEGATE_CHROMIUM } : {}) });
});
after(async () => { await browser?.close(); await vite?.close(); });
for (const width of [390, 1440]) test(`guided transfer retains source and gates cleanup (${width}px)`, async () => {
  const page = await browser.newPage({ viewport: { width, height: 1000 } });
  const errors = [], writes = [];
  const stamp = new Date().toISOString();
  const account = { id: 'account', displayName: 'Canary', status: 'active', serverId: 'source', createdAt: stamp, updatedAt: stamp };
  const nodes = ['source', 'target'].map(id => ({ id, name: id === 'source' ? 'Source node' : 'Target node', deploymentRole: id === 'source' ? 'hybrid' : 'vpn', status: 'active' }));
  let transfer = null;
  let failedCompletion = false;
  page.on('pageerror', e => errors.push(e.message));
  await page.addInitScript(() => { localStorage.setItem('routegate.locale', 'en'); localStorage.setItem('routegate.auth.token', 'fixture'); });
  page.on('dialog', dialog => dialog.accept());
  await page.route(url => url.pathname.startsWith('/api/'), async route => {
    const path = new URL(route.request().url()).pathname, method = route.request().method();
    const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    if (method !== 'GET') writes.push({ path, method, body: route.request().postDataJSON() });
    if (path === '/api/admin/health') return json({ status: 'ok' });
    if (path === '/api/admin/me') return json({ user: { id: 'admin', roles: ['super_admin'] } });
    if (path === '/api/v1/servers') return json({ items: nodes });
    if (path === '/api/v1/vpn-accounts') return json({ items: [account], total: 1, page: 1, pageSize: 50, totalPages: 1 });
    if (path === '/api/v1/vpn-accounts/account') return json(account);
    if (path.endsWith('/routing-policy')) return json({ routingProfileSource: 'none', currentServerInGroup: false, automaticSelection: false, automaticSelectionPolicy: { enabled: false, allowDegraded: false, cooldownSeconds: 300 }, clientRoutingSupported: true });
    if (path === '/api/v1/routing-profiles' || path === '/api/v1/node-groups' || path.endsWith('/devices')) return json({ items: [] });
    if (path.endsWith('/notes')) return json({ notes: '', updatedAt: stamp });
    if (path.endsWith('/subscription-token')) return json({ hasActiveToken: true });
    if (path.endsWith('/traffic')) return json({ period: { from: stamp, to: stamp }, usage: { totalBytes: 0, txBytes: 0, rxBytes: 0 }, limit: null });
    if (path.endsWith('/client-profile')) return json({ connectionStatus: 'ready', activeProtocol: 'vless', profile: { id: 'profile', vpnAccountId: 'account', protocol: 'vless', enabledProtocols: ['vless'], activeProtocols: ['vless'], clientType: 'hiddify', deviceType: 'ios', fingerprintMode: 'auto', fingerprint: 'firefox', spiderX: '/' } });
    if (path.endsWith('/client-connection')) return json({ profile: {}, connections: [] });
    if (path.endsWith('/transfer')) {
      if (method === 'POST') {
        assert.equal(route.request().postDataJSON().targetServerId, 'target');
        transfer = { id: 'operation', accountId: 'account', sourceServerId: 'source', targetServerId: 'target', state: 'target_applying', lastError: '', updatedAt: stamp, cutoverAt: null, completedAt: null, devices: [] };
        return json(transfer, 202);
      }
      return json({ transfer, requiresTransfer: true });
    }
    if (path.endsWith('/transfer/operation') && method === 'POST') {
      const body = route.request().postDataJSON();
      if (body.action === 'finish' && !failedCompletion) {
        failedCompletion = true;
        transfer.lastError = 'apply_failed_or_version_changed';
        return json({ message: 'transfer safety gate blocked the action: apply_failed_or_version_changed' }, 409);
      }
      if (body.action === 'retry') transfer.lastError = '';
      if (body.action === 'finish') { transfer.state = 'complete'; transfer.completedAt = stamp; }
      if (body.action === 'verify') transfer.state = 'target_ready';
      if (body.action === 'cutover') { transfer.state = 'client_refresh_pending'; transfer.cutoverAt = stamp; account.serverId = 'target'; transfer.devices = [{ id: 'phone', name: 'iPhone', lastRequestedAt: null, requestedAfterCutover: false }]; }
      if (body.action === 'cleanup') { assert.equal(body.confirmed, true); transfer.state = 'source_cleaning'; }
      return json(transfer);
    }
    return json({ message: `Unmocked ${method} ${path}` }, 404);
  });
  try {
    await page.goto(`${origin}/vpn-accounts/account/routing`);
    const placement = page.locator('.vpn-account-routing-workspace > .workspace-section');
    await placement.getByRole('combobox').selectOption('target');
    await placement.getByRole('button', { name: 'Prepare transfer', exact: true }).click();
    await placement.getByRole('button', { name: 'Verify target', exact: true }).waitFor();
    assert.equal(account.serverId, 'source');
    assert.equal(await placement.getByText('Preparation started. Next, verify the target.', { exact: true }).count(), 0);
    assert.ok(!writes.some(w => w.method === 'PATCH'));
    assert.equal(await placement.getByRole('combobox').isDisabled(), true);
    await placement.getByRole('button', { name: 'Verify target', exact: true }).click();
    await placement.getByRole('button', { name: 'Switch subscription', exact: true }).waitFor();
    await page.reload();
    await placement.getByRole('button', { name: 'Switch subscription', exact: true }).click();
    const cleanup = placement.getByRole('button', { name: 'Remove source access', exact: true });
    await cleanup.waitFor();
    assert.equal(account.serverId, 'target');
    assert.equal(await cleanup.isDisabled(), true);
    await placement.getByText('iPhone: no request observed after cutover', { exact: true }).waitFor();
    if (process.env.ROUTEGATE_TRANSFER_SCREENSHOT_DIR) await page.screenshot({ path: `${process.env.ROUTEGATE_TRANSFER_SCREENSHOT_DIR}/transfer-${width}.png`, fullPage: true });
    await placement.getByRole('checkbox').check();
    await cleanup.click();
    await placement.getByRole('button', { name: 'Verify completion', exact: true }).waitFor();
    assert.equal(transfer.state, 'source_cleaning');
    await placement.getByRole('button', { name: 'Verify completion', exact: true }).click();
    const panel = placement.locator('.account-transfer-panel');
    await panel.getByRole('button', { name: 'Retry failed apply', exact: true }).waitFor();
    const failureCopy = 'Check the apply result. A failed apply can be retried; a changed version requires restoring verified state.';
    await page.waitForFunction(copy => [...document.querySelectorAll('.account-transfer-panel .form-message')].filter(n => n.textContent === copy).length === 1, failureCopy);
    assert.equal(await panel.locator('.form-message-error').count(), 0);
    if (process.env.ROUTEGATE_TRANSFER_SCREENSHOT_DIR) await page.screenshot({ path: `${process.env.ROUTEGATE_TRANSFER_SCREENSHOT_DIR}/transfer-retry-${width}.png`, fullPage: true });
    await panel.getByRole('button', { name: 'Retry failed apply', exact: true }).click();
    await page.waitForFunction(() => !document.querySelector('.account-transfer-panel .form-message'));
    await panel.getByRole('button', { name: 'Verify completion', exact: true }).click();
    await panel.getByText('Transfer complete', { exact: true }).waitFor();
    assert.deepEqual(errors, []);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 2));
  } finally { await page.close(); }
});

for (const width of [390, 1440]) test(`account deletion is blocked until an RG-140 transfer is completed (${width}px)`, async () => {
  const page = await browser.newPage({ viewport: { width, height: 960 } });
  const errors = [], writes = [];
  const stamp = new Date().toISOString();
  let state = 'client_refresh_pending';
  let transferLookupFails = false;
  const account = { id: 'canary', displayName: 'fi-test', username: 'fi-test', status: 'active', serverId: 'us', createdAt: stamp, updatedAt: stamp };
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem('routegate.locale', 'en');
    localStorage.setItem('routegate.auth.token', 'fixture');
  });
  page.on('dialog', dialog => dialog.accept());
  await page.route(url => url.pathname.startsWith('/api/'), async route => {
    const path = new URL(route.request().url()).pathname;
    const method = route.request().method();
    const json = (value, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(value) });
    if (method !== 'GET') writes.push({ path, method });
    if (path === '/api/admin/health') return json({ status: 'ok' });
    if (path === '/api/admin/me') return json({ user: { id: 'admin', roles: ['super_admin'] } });
    if (path === '/api/v1/vpn-accounts') return json({ items: [account], total: 1, page: 1, pageSize: 50, totalPages: 1 });
    if (path === '/api/v1/vpn-accounts/canary') return json(account);
    if (path === '/api/v1/vpn-accounts/canary/notes') return json({ notes: '', updatedAt: stamp });
    if (path === '/api/v1/vpn-accounts/canary/transfer') {
      if (transferLookupFails) return json({ message: 'API unavailable' }, 503);
      return json({ transfer: state ? {
        id: 'transfer', accountId: 'canary', state,
        sourceServerId: 'fi', targetServerId: 'us',
        lastError: '', updatedAt: stamp, cutoverAt: stamp,
        completedAt: state === 'complete' ? stamp : null, devices: [],
      } : null, requiresTransfer: true });
    }
    if (path === '/api/v1/servers') return json({ items: [{ id: 'us', name: 'US' }, { id: 'fi', name: 'FI' }] });
    return json({ message: `Unmocked ${method} ${path}` }, 404);
  });
  try {
    await page.goto(`${origin}/vpn-accounts/canary/settings`);
    const action = page.getByRole('button', { name: 'Delete account permanently' });
    await action.waitFor();
    await page.getByText('The account transfer is not complete.', { exact: false }).waitFor();
    assert.equal(await action.isDisabled(), true);
    const link = page.getByRole('link', { name: 'Open transfer steps' });
    assert.equal(await link.getAttribute('href'), '/vpn-accounts/canary/routing');
    assert.equal(writes.length, 0, 'disabled delete cannot write or erase an active transfer');
    assert.deepEqual(errors, []);

    transferLookupFails = true;
    await page.reload();
    await page.getByText('Could not verify the transfer state.', { exact: false }).waitFor();
    assert.equal(await action.isDisabled(), true, 'unknown transfer state must fail closed');
    assert.equal(writes.length, 0);

    transferLookupFails = false;
    state = 'complete';
    await page.reload();
    await action.waitFor();
    await page.waitForFunction(() => {
      const btn = [...document.querySelectorAll('button')].find(x => x.textContent?.includes('Delete account permanently'));
      return btn && !btn.disabled;
    });
    assert.equal(await page.getByText('The account transfer is not complete.', { exact: false }).count(), 0);
    assert.equal(writes.length, 0, 'terminal transfer does not automatically delete the account');
    assert.deepEqual(errors, []);
  } finally {
    await page.close();
  }
});
