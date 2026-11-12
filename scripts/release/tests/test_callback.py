import importlib.util
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('facts', pathlib.Path(__file__).resolve().parents[1] / 'facts.py')
facts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(facts)

class CallbackFacts(unittest.TestCase):
    def test_only_expected_task_path_and_success_confirms_image(self):
        s = {'checks': dict.fromkeys(('switch','image','internal','external','routes'), 'not_run')}
        facts.observe(s, 'roles/app/tasks/check-backend-image.yml:50', 'Backend image check', 'ok', {})
        self.assertEqual(s['checks']['switch'], 'passed')
        self.assertEqual(s['checks']['image'], 'passed')
        facts.observe(s, 'fake.yml:1', 'Backend internal HTTP check', 'ok', {})
        self.assertEqual(s['checks']['internal'], 'not_run')

    def test_unreachable_or_skipped_is_not_success_and_internal_fact_survives(self):
        s = {'checks': dict.fromkeys(('switch','image','internal','external','routes'), 'not_run')}
        facts.observe(s, 'roles/app/tasks/probe-backend.yml:3', 'Backend internal HTTP check', 'ok', {})
        facts.observe(s, 'roles/app/tasks/probe-bff.yml:8', 'Backend external BFF check', 'unreachable', {})
        self.assertEqual(s['checks']['internal'], 'passed')
        self.assertEqual(s['checks']['external'], 'unknown')
        self.assertEqual(s['error_stage'], 'external')

    def test_route_loop_partial_result_never_passes(self):
        s = {'checks': dict.fromkeys(('switch','image','internal','external','routes'), 'not_run')}
        name = 'Go API, unknown API paths and the internal health are not public'
        facts.observe(s, 'roles/app/tasks/probe-bff.yml:110', name, 'ok', {'results':[{'item':'/health'}]})
        self.assertNotEqual(s['checks']['routes'], 'passed')
        facts.observe(s, 'roles/app/tasks/probe-bff.yml:110', name, 'ok', {'results':[{'item':x} for x in ['/api/v1/users/me','/api/unknown','/health']]})
        self.assertEqual(s['checks']['routes'], 'passed')

    def test_late_failure_does_not_erase_completed_mandatory_checks(self):
        s = {'checks': dict.fromkeys(('switch','image','internal','external','routes'), 'passed')}
        facts.observe(s, 'roles/app/tasks/probe-bff.yml:110', 'Go API, unknown API paths and the internal health are not public', 'ok', {'results':[{'item':x} for x in ['/api/v1/users/me','/api/unknown','/health']]})
        facts.observe(s, 'late.yml:1', 'Late diagnostic', 'failed', {})
        self.assertTrue(s['mandatory_complete'])

    def test_matching_running_image_can_be_confirmed_before_failed_health(self):
        s={'checks':dict.fromkeys(('switch','image','internal','external','routes'),'not_run')}
        image='sha256:'+'a'*64
        facts.observe(s,'roles/app/tasks/check-backend-image.yml:1','Read the backend container image and health','ok',{'stdout':image+' true unhealthy'})
        facts.observe(s,'roles/app/tasks/check-backend-image.yml:2','Read the local ID of the selected backend digest','ok',{'stdout':image})
        facts.observe(s,'roles/app/tasks/check-backend-image.yml:3','Backend image check','failed',{})
        self.assertEqual(s['checks']['switch'],'passed')
        self.assertEqual(s['checks']['image'],'failed')
        self.assertNotIn('stdout',s)
