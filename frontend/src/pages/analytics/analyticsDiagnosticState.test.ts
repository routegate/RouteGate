import assert from 'node:assert/strict';
import test from 'node:test';
import { diagnosticRequestErrorKey, isDiagnosticActive } from './analyticsDiagnosticState.ts';

test('queued and running Agent diagnostics are not completed checks', () => {
  assert.equal(isDiagnosticActive('queued'), true);
  assert.equal(isDiagnosticActive('running'), true);
  assert.equal(isDiagnosticActive('succeeded'), false);
  assert.equal(isDiagnosticActive('failed'), false);
  assert.equal(isDiagnosticActive(undefined), false);
});

test('a busy Agent produces a task-concurrency message, not a certificate error', () => {
  assert.equal(diagnosticRequestErrorKey({ status: 409, code: 'agent_operation_busy' }), 'analytics.diagnosticBusy');
});

test('an older Agent missing the profile is not diagnosed as an invalid certificate', () => {
  assert.equal(diagnosticRequestErrorKey({ status: 409, code: 'diagnostic_unavailable' }), 'analytics.diagnosticUnavailable');
});

test('authorization and unknown transport failures stay distinct', () => {
  assert.equal(diagnosticRequestErrorKey({ status: 401 }), 'analytics.diagnosticUnauthorized');
  assert.equal(diagnosticRequestErrorKey({ status: 403 }), 'analytics.diagnosticUnauthorized');
  assert.equal(diagnosticRequestErrorKey({ status: 500, code: 'database_error' }), 'analytics.diagnosticRequestFailed');
  assert.equal(diagnosticRequestErrorKey(new Error('network')), 'analytics.diagnosticRequestFailed');
});
