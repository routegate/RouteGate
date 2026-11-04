import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
import os
from unittest import mock

SPEC = importlib.util.spec_from_file_location('traffic_rollout', pathlib.Path(__file__).with_name('traffic-accounting-rollout.py'))
rollout = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(rollout)

class TrafficRolloutTests(unittest.TestCase):
    def test_database_uri_becomes_decoded_libpq_parameters_not_database_name(self):
        env = rollout.database_environment('postgres://test-user:p%40%3Ass@127.0.0.1:5432/traffic%2Dtest?sslmode=disable')
        self.assertEqual(env['PGHOST'], '127.0.0.1')
        self.assertEqual(env['PGPORT'], '5432')
        self.assertEqual(env['PGUSER'], 'test-user')
        self.assertEqual(env['PGPASSWORD'], 'p@:ss')
        self.assertEqual(env['PGDATABASE'], 'traffic-test')
        self.assertEqual(env['PGSSLMODE'], 'disable')
        self.assertIn('default_transaction_read_only=on', env['PGOPTIONS'])
        with self.assertRaises(rollout.Stop):
            rollout.database_environment('postgres://user:password@localhost/db?options=unsafe')

    @unittest.skipUnless(os.environ.get('ROUTEGATE_TEST_DATABASE_URL'), 'requires test PostgreSQL')
    def test_database_connection_with_real_postgresql_is_read_only(self):
        with mock.patch.dict(os.environ, {'ROUTEGATE_DATABASE_URL': os.environ['ROUTEGATE_TEST_DATABASE_URL']}):
            self.assertEqual(rollout.sql('SELECT 1'), '1')
            self.assertEqual(rollout.sql('SHOW default_transaction_read_only'), 'on')
    def test_preserve_existing_onboarding_metadata_without_guessing(self):
        base = 'https://us.routegate.org/bootstrap/' + 'a' * 40
        digest = 'b' * 64
        prefix = 'github.com/ikaevus/routegate/backend/internal/buildinfo.'
        sample = ('build\t-ldflags="-X ' + prefix + 'AgentBundleBaseURL=' + base + ' -X ' + prefix + 'AgentInstallerSHA256=' + digest + '"\n').encode()
        self.assertEqual(rollout.bootstrap_metadata(sample), {'AgentBundleBaseURL': base, 'AgentInstallerSHA256': digest})
        with self.assertRaises(rollout.Stop):
            rollout.bootstrap_metadata(sample.replace(b'https://us.routegate.org', b'https://other.example'))
        with self.assertRaises(rollout.Stop):
            rollout.bootstrap_metadata(b'no metadata')
    def test_read_only_diagnosis_reports_actual_pipeline_gaps_without_binary_scraping(self):
        agent_id = '11111111-1111-4111-8111-111111111111'
        server_id = '22222222-2222-4222-8222-222222222222'
        with tempfile.TemporaryDirectory() as directory:
            active = pathlib.Path(directory) / 'config.json'
            active.write_text('{}')
            cfg = pathlib.Path(directory) / 'agent.yaml'
            cfg.write_text('agent_id: "' + agent_id + '"\nserver_id: "' + server_id +
                           '"\ntraffic_collection_enabled: false\nactive_config_path: ' + str(active) + '\n')
            queries = []

            def read_only_sql(statement):
                queries.append(statement)
                self.assertTrue(statement.strip().upper().startswith('SELECT'))
                if 'FROM agents a JOIN servers' in statement:
                    return '1'
                if 'max(version)' in statement:
                    return rollout.OLD_SCHEMA
                if 'FROM vpn_account_transfers' in statement:
                    return '0'
                if 'FROM config_apply_jobs' in statement:
                    return '0'
                if 'FROM traffic_usage_events' in statement:
                    return '0'
                if 'FROM traffic_usage_daily' in statement:
                    return '0'
                self.fail('unexpected SQL statement')

            with mock.patch.object(rollout, 'CONFIG', cfg), \
                 mock.patch.object(rollout, 'sql', side_effect=read_only_sql), \
                 mock.patch.object(rollout, 'run', return_value='sing-box 1.12.12\nTags: with_v2ray_api,with_quic'), \
                 mock.patch.object(rollout, 'bootstrap_metadata', side_effect=AssertionError('binary metadata scraper must not run')), \
                 mock.patch('sys.stdout', new_callable=io.StringIO) as output:
                report = rollout.diagnose()
                self.assertEqual(json.loads(output.getvalue()), report)

            self.assertEqual(len(queries), 6)
            self.assertEqual(report['mode'], 'read_only')
            self.assertEqual(report['schema'], rollout.OLD_SCHEMA)
            self.assertFalse(report['schema160_ready'])
            self.assertTrue(report['runtime_stats_supported'])
            self.assertFalse(report['agent_collection_enabled'])
            self.assertFalse(report['runtime_stats_enabled'])
            self.assertEqual(report['recent_native_reports_30d'], 0)
            self.assertFalse(report['dashboard_daily_available_from_database'])
            self.assertTrue(report['no_service_or_config_changes'])
            self.assertTrue(report['live_api_not_checked'])
            # Explicit proof of what the report did not expose.
            self.assertNotIn(agent_id, output.getvalue())
            self.assertNotIn(server_id, output.getvalue())

    def test_diagnosis_marks_unknown_runtime_probe_and_malformed_active_config(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            cfg = root / 'agent.yaml'
            cfg.write_text(
                'agent_id: 11111111-1111-4111-8111-111111111111\n'
                'server_id: 22222222-2222-4222-8222-222222222222\n'
                'traffic_collection_enabled: true\n'
                'active_config_path: ' + str(root / 'missing-config.json') + '\n'
            )
            def db(statement):
                if 'FROM agents a JOIN servers' in statement:
                    return '1'
                if 'max(version)' in statement:
                    return rollout.SCHEMA
                if 'FROM vpn_account_transfers' in statement:
                    return '2'
                if 'FROM config_apply_jobs' in statement:
                    return '1'
                if 'FROM traffic_usage_events' in statement:
                    return '4'
                if 'FROM traffic_usage_daily' in statement:
                    return '3'
                self.fail('unknown SQL')
            with mock.patch.object(rollout, 'CONFIG', cfg), \
                 mock.patch.object(rollout, 'sql', side_effect=db), \
                 mock.patch.object(rollout, 'run', side_effect=rollout.Stop('runtime_unavailable')), \
                 mock.patch('sys.stdout', new_callable=io.StringIO):
                report = rollout.diagnose()
            self.assertTrue(report['schema160_ready'])
            self.assertIsNone(report['runtime_stats_supported'])
            self.assertIsNone(report['runtime_stats_enabled'])
            self.assertIsNone(report['runtime_stats_loopback_configured'])
            self.assertEqual(report['unfinished_transfers'], 2)
            self.assertEqual(report['unfinished_apply_jobs'], 1)
            self.assertEqual(report['recent_native_reports_30d'], 4)
            self.assertTrue(report['dashboard_daily_available_from_database'])
            self.assertEqual(report['dashboard_daily_rows_30d'], 3)

    def test_diagnosis_does_not_trust_unbound_us_agent(self):
        with tempfile.TemporaryDirectory() as directory:
            cfg = pathlib.Path(directory) / 'agent.yaml'
            cfg.write_text(
                'agent_id: 11111111-1111-4111-8111-111111111111\n'
                'server_id: 22222222-2222-4222-8222-222222222222\n'
            )
            with mock.patch.object(rollout, 'CONFIG', cfg), \
                 mock.patch.object(rollout, 'sql', return_value='0'):
                with self.assertRaisesRegex(rollout.Stop, 'local_agent_us_server_binding_not_verified'):
                    rollout.diagnose()

    def test_production_workflow_allows_inspection_only(self):
        workflow = pathlib.Path(__file__).resolve().parents[1] / '.github/workflows/traffic-accounting-rollout.yml'
        content = workflow.read_text()
        self.assertIn('options: [inspect]', content)
        self.assertIn('[[ "$MODE" == inspect ]]', content)
        self.assertIn('concurrency:\n  group: routegate-production-like-deploy', content)
        self.assertIn('StrictHostKeyChecking=yes', content)
        self.assertNotIn('options: [inspect, install', content)
        self.assertNotIn('BOOTSTRAP_BASE_URL', content)
        self.assertNotIn('Build reproducible control-plane payload', content)

    def test_non_inspection_modes_still_refuse_before_any_mutation(self):
        for mode in ('install', 'apply', 'invalid'):
            with self.subTest(mode=mode), \
                 mock.patch.object(rollout.os, 'geteuid', return_value=0), \
                 mock.patch.object(rollout.sys, 'argv', ['runner.py', mode, '/tmp/ignored', '7']), \
                 mock.patch.object(rollout, 'install', side_effect=AssertionError('no install')), \
                 mock.patch.object(rollout, 'apply_metrics', side_effect=AssertionError('no apply')):
                with self.assertRaisesRegex(rollout.Stop, 'install_and_apply_disabled_pending_bootstrap_identity_repair'):
                    rollout.main()

    def test_enable_only_changes_collection_flag(self):
        old = b'agent_token: "private-token"\ntraffic_collection_enabled: false\nserver_id: "unchanged"\n'
        new = rollout.enabled_config(old)
        self.assertEqual(new, old.replace(b'enabled: false', b'enabled: true'))
        with self.assertRaises(rollout.Stop):
            rollout.enabled_config(old + b'traffic_collection_enabled: true\n')

    def test_runtime_comparison_ignores_statistics_but_not_credentials_or_routes(self):
        old = {'singBox': {'inbounds': [{'users': [{'uuid': 'private-uuid'}]}], 'route': {'final': 'direct'}}}
        import copy
        new = copy.deepcopy(old)
        new['singBox']['experimental'] = {'v2ray_api': {'listen': '127.0.0.1:18085'}}
        self.assertEqual(rollout.comparable_runtime(old), rollout.comparable_runtime(new))
        self.assertIn('v2ray_api', new['singBox']['experimental'])
        new['singBox']['inbounds'][0]['users'][0]['uuid'] = 'different'
        self.assertNotEqual(rollout.comparable_runtime(old), rollout.comparable_runtime(new))
        new = copy.deepcopy(old)
        new['singBox']['route']['final'] = 'block'
        self.assertNotEqual(rollout.comparable_runtime(old), rollout.comparable_runtime(new))

    def test_success_only_restarts_control_plane_and_retains_backups(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            target = root / 'binary'
            target.write_bytes(b'old')
            target.chmod(0o755)
            commands = []
            rollout.install_files({target: b'new'}, root / 'backup', health=lambda: None,
                                  protected=lambda: ('unchanged',), command=commands.append)
            self.assertEqual(target.read_bytes(), b'new')
            self.assertEqual((root / 'backup' / '0').read_bytes(), b'old')
            self.assertEqual(target.stat().st_mode & 0o777, 0o755)
            self.assertTrue(all(command[-1] in ('routegate-manager', 'routegate-agent') for command in commands))

    def test_install_failure_restores_binaries_and_config(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            first, second = root / 'first', root / 'second'
            first.write_bytes(b'old-first')
            second.write_bytes(b'old-second')
            atomic = rollout.atomic
            failed = False
            def injected(path, data, mode):
                nonlocal failed
                if path == second and data == b'new-second' and not failed:
                    failed = True
                    raise OSError('injected')
                return atomic(path, data, mode)
            with mock.patch.object(rollout, 'atomic', injected):
                with self.assertRaises(OSError):
                    rollout.install_files({first: b'new-first', second: b'new-second'}, root / 'backup',
                                          health=lambda: None, recover_health=lambda: None,
                                          protected=lambda: ('unchanged',), command=lambda args: None)
            self.assertEqual(first.read_bytes(), b'old-first')
            self.assertEqual(second.read_bytes(), b'old-second')

    def test_failed_new_manager_health_runs_distinct_recovery_probe(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            target = root / 'binary'
            target.write_bytes(b'old')
            recovered = []
            def fail():
                raise rollout.Stop('injected-health-failure')
            with self.assertRaises(rollout.Stop):
                rollout.install_files({target: b'new'}, root / 'backup', health=fail,
                                      recover_health=lambda: recovered.append(True),
                                      protected=lambda: ('unchanged',), command=lambda args: None)
            self.assertEqual(target.read_bytes(), b'old')
            self.assertEqual(recovered, [True])

    def test_apply_refuses_any_additional_runtime_change(self):
        self.exercise_apply(additional_change=True)

    def test_apply_uses_normal_manager_job_then_verifies_native_reports(self):
        self.exercise_apply(additional_change=False)

    def exercise_apply(self, additional_change):
        import json
        import copy
        old = {'singBox': {'inbounds': [{'users': [{'uuid': 'private-uuid'}]}], 'route': {'final': 'direct'}}}
        new = copy.deepcopy(old)
        new['singBox']['experimental'] = {'v2ray_api': {'listen': '127.0.0.1:18085', 'stats': {'enabled': True}}}
        if additional_change:
            new['singBox']['route']['final'] = 'block'
        previous_id = '11111111-1111-4111-8111-111111111111'
        version_id = '22222222-2222-4222-8222-222222222222'
        job_id = '33333333-3333-4333-8333-333333333333'
        agent_id = '44444444-4444-4444-8444-444444444444'
        server_id = '55555555-5555-4555-8555-555555555555'
        calls = []
        def api(path, payload=None, token=None):
            calls.append(path)
            if path.endswith('/versions'):
                return {'currentConfigVersionId': previous_id}
            if path.endswith('/render'):
                return {'configVersion': {'id': version_id}}
            if path.endswith('/validate'):
                return {'validationResult': {'valid': True}}
            if path.endswith('/apply'):
                return {'job': {'id': job_id}}
            if '/apply-jobs/' in path:
                return {'status': 'succeeded'}
            if path == '/api/v1/dashboard/traffic':
                return {'dailyAvailable': True, 'monthlyAvailable': True}
            self.fail('unexpected API operation')
        def sql(query):
            if 'max(version)' in query:
                return rollout.SCHEMA
            if 'rendered_config' in query:
                return json.dumps(old if previous_id in query else new)
            return '1'
        with tempfile.TemporaryDirectory() as directory:
            active = pathlib.Path(directory) / 'active.json'
            active.write_text(json.dumps(old['singBox']))
            values = {'traffic_collection_enabled': 'true', 'active_config_path': str(active)}
            with mock.patch.object(rollout, 'inspect', return_value=(values, agent_id, server_id)), \
                 mock.patch.object(rollout, 'login', return_value='private-token'), \
                 mock.patch.object(rollout, 'sql', side_effect=sql), \
                 mock.patch.object(rollout, 'api', side_effect=api), \
                 mock.patch.object(rollout, 'run', return_value=''):
                if additional_change:
                    with self.assertRaisesRegex(rollout.Stop, 'additional_runtime_changes_refused'):
                        rollout.apply_metrics()
                    self.assertFalse(any(path.endswith('/apply') for path in calls))
                else:
                    rollout.apply_metrics()
                    self.assertTrue(any(path.endswith('/apply') for path in calls))
                    self.assertIn('/api/v1/dashboard/traffic', calls)

if __name__ == '__main__':
    unittest.main()
