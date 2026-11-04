import assert from 'node:assert/strict';
import test from 'node:test';
import { resolveTrafficWidgetStatus } from './trafficOverviewState.ts';

const zeroDay = { date: '2026-11-05', rxBytes: 0, txBytes: 0, totalBytes: 0 };
const usageDay = { date: '2026-11-05', rxBytes: 10, txBytes: 20, totalBytes: 30 };

test('pending or failed API never pretends that traffic is zero or unavailable only by choice', () => {
  assert.equal(resolveTrafficWidgetStatus('pending', undefined, undefined), 'loading');
  assert.equal(resolveTrafficWidgetStatus('error', undefined, undefined), 'error');
  assert.equal(resolveTrafficWidgetStatus('error', true, [usageDay]), 'error', 'failed refresh should not silently present cached values as current');
});

test('absence of measurements is determined by dailyAvailable, not generated placeholder rows', () => {
  assert.equal(resolveTrafficWidgetStatus('success', false, Array(30).fill(zeroDay)), 'no-data');
  assert.equal(resolveTrafficWidgetStatus('success', undefined, [usageDay]), 'no-data');
});

test('valid measurements, including legitimate zero traffic, render the chart', () => {
  assert.equal(resolveTrafficWidgetStatus('success', true, [zeroDay]), 'ready');
  assert.equal(resolveTrafficWidgetStatus('success', true, [usageDay]), 'ready');
});

test('malformed or missing successful snapshots do not render fabricated data', () => {
  assert.equal(resolveTrafficWidgetStatus('success', true, undefined), 'error');
  assert.equal(resolveTrafficWidgetStatus('success', true, []), 'error');
  assert.equal(resolveTrafficWidgetStatus('success', true, [{ ...usageDay, totalBytes: -1 }]), 'error');
  assert.equal(resolveTrafficWidgetStatus('success', true, [{ ...usageDay, rxBytes: Number.NaN }]), 'error');
  assert.equal(resolveTrafficWidgetStatus('success', true, [{ ...usageDay, date: 'invalid' }]), 'error');
});
