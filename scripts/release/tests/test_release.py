import json
import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('release', ROOT / 'release.py')
r = importlib.util.module_from_spec(spec)
if spec.loader:
    spec.loader.exec_module(r)

A, B, C = 'a' * 40, 'b' * 40, 'c' * 40
D, E = r.PACKAGE + '@sha256:' + 'd' * 64, r.PACKAGE + '@sha256:' + 'e' * 64

def record(sha=A, image=D, state='success', identifier=1):
    return {'id': identifier, 'sha': sha, 'image_ref': image, 'state': state, 'trusted': True}

class Selection(unittest.TestCase):
    def test_auto_after_success_skips_same_digest(self):
        self.assertEqual(r.select_target([record(B, D), record(A, D), record(C, E)])[0], C)

    def test_failure_restores_last_success(self):
        self.assertEqual(r.select_target([record(B, E, 'error'), record()]), (A, D))

    def test_unknown_latest_refuses_auto_but_explicit_success_works(self):
        h = [record(B, E, 'in_progress'), record()]
        with self.assertRaises(r.ReleaseError): r.select_target(h)
        self.assertEqual(r.select_target(h, A), (A, D))
        h[0]['trusted'] = False
        with self.assertRaises(r.ReleaseError): r.select_target(h)

    def test_ambiguous_sha_and_unavailable_history_fail_closed(self):
        for h in [[], [record(A, D), record(A, E)]]:
            with self.assertRaises(r.ReleaseError): r.select_target(h, A)
        for sha in ['main', 'a'*7, A+'\n', 'g'*40]:
            with self.assertRaises(r.ReleaseError): r.validate_sha(sha)
        for image in [D.replace('/backend@', '/bff@'), D[:-1], r.PACKAGE+':main']:
            with self.assertRaises(r.ReleaseError): r.validate_image(image)

    def test_healthy_requires_every_check_and_process_success(self):
        state = r.initial_state('deploy', 10, 2)
        for key in r.CHECKS: state['checks'][key] = 'passed'
        state['ansible_rc'] = 0
        self.assertEqual(r.outcome(state), 'success')
        state['checks']['external'] = 'failed'
        self.assertEqual(r.outcome(state), 'failure')
        state['checks']['external'] = 'unknown'
        self.assertEqual(r.outcome(state), 'error')
        state['checks']['external'] = 'passed'; state['ansible_rc'] = None
        self.assertEqual(r.outcome(state), 'error')

if __name__ == '__main__': unittest.main()

class FakeGitHub(r.GitHub):
    def __init__(self, records=None):
        self.prefix = f'/repos/{r.REPOSITORY}'
        self.records = records or []
        self.posts = []
        self.fail_create = False
        self.page_failure = False
    def pages(self, path, field=None):
        if self.page_failure: raise r.ReleaseError('GitHub HTTP 403')
        if '/jobs' in path: return [{'name':'deploy / deploy','steps':[{'name':'Switch backend','conclusion':'success'},{'name':'Health check','conclusion':'success'}]}]
        if '/statuses' in path: return [{'state':'success','creator':{'login':'github-actions[bot]'},'log_url':r.RUN_ROOT+'10/attempts/1','environment_url':'https://cellestial.ru'}]
        return self.records
    def get(self, path):
        if '/workflows/' in path: return {'id': 5}
        return {'id':10, 'run_attempt':1, 'path':'.github/workflows/ci.yml', 'workflow_id':5,
                'head_branch':'main', 'event':'push', 'head_sha':A,
                'repository':{'full_name':r.REPOSITORY},'head_repository':{'full_name':r.REPOSITORY}}
    def request(self, path, body=None):
        self.posts.append((path, body))
        if path.endswith('/deployments'):
            raw = {'id':99,'sha':body['ref'],'ref':body['ref'],'environment':'production','creator':{'login':'github-actions[bot]'},'payload':body['payload']}
            self.records.append(raw)
            if self.fail_create: raise r.ReleaseError('lost response')
            return raw, None
        return {}, None

class Producer(unittest.TestCase):
    def state(self):
        s = r.initial_state('rollback',10,1)
        s.update(target_sha=A,image_ref=D,base_sha=B)
        return s
    def test_creation_contract_and_target_sha(self):
        api = FakeGitHub(); state = self.state()
        self.assertEqual(api.ensure_deployment(state),99)
        body = api.posts[0][1]
        self.assertEqual(body['ref'],A)
        self.assertEqual(body['task'],'rollback')
        self.assertEqual(body['payload']['notification'],{'run_attempt':1,'base_sha':B,'report_url':r.RUN_ROOT+'10#summary'})
        state['deployment_id']=99;api.status(state,'success')
        self.assertEqual(api.posts[-1][1]['log_url'],r.RUN_ROOT+'10/attempts/1')
        self.assertFalse(api.posts[-1][1]['auto_inactive'])
    def test_lost_create_response_is_recovered_without_duplicate_post(self):
        api=FakeGitHub();api.fail_create=True
        self.assertEqual(api.ensure_deployment(self.state()),99)
        self.assertEqual(api.ensure_deployment(self.state()),99)
        self.assertEqual(len(api.posts),1)
        s=self.state();s['run_attempt']=2
        self.assertEqual(api.ensure_deployment(s),99)
        self.assertEqual(len(api.posts),2)
    def test_api_failure_is_not_empty_history(self):
        api=FakeGitHub();api.page_failure=True
        with self.assertRaises(r.ReleaseError):api.history()
    def test_unknown_record_is_preserved_as_barrier(self):
        api=FakeGitHub([{'id':1,'sha':A,'payload':{},'created_at':'2026-11-10'}])
        history=api.history()
        self.assertFalse(history[0]['trusted'])
        with self.assertRaises(r.ReleaseError):r.select_target(history)
    def test_summary_partial_facts_and_rollback_direction(self):
        s=self.state();s['checks']['internal']='passed';s['checks']['external']='failed'
        text=r.summary(s)
        self.assertIn('Снимаемые изменения',text)
        self.assertIn('Внутренний Go с gateway | passed',text)
        self.assertIn('Внешний BFF → Go → PostgreSQL | failed',text)
        self.assertIn('История изменений неполная',text)
        self.assertIn('Health Check: ❌',text)
    def test_consumer_fixture_accepted_without_frontend_assets(self):
        fixture=json.loads((ROOT/'tests/fixtures/backend-release-contract.json').read_text())
        row=fixture['previous'];api=FakeGitHub()
        api.get=lambda path: {'id':1} if '/workflows/' in path else fixture['run'] | {'id':10,'head_sha':row['sha']}
        result=api.record(row)
        self.assertTrue(result['trusted'])
        row['ref']=B
        self.assertFalse(api.record(row)['trusted'])

class HistoryProof(unittest.TestCase):
    def fixture(self):
        return json.loads((ROOT/'tests/fixtures/backend-release-contract.json').read_text())
    def test_skipped_health_and_foreign_status_never_confirm_history(self):
        fixture=self.fixture();raw=fixture['deployment'];api=FakeGitHub()
        api.get=lambda path: {'id':1} if '/workflows/' in path else fixture['run']
        def pages(path,field=None):
            if '/jobs' in path:return [{'name':'deploy / deploy','steps':[{'name':'Switch backend','conclusion':'skipped'},{'name':'Health check','conclusion':'skipped'}]}]
            return [{'state':'success','creator':{'login':'other'},'log_url':r.RUN_ROOT+'20/attempts/2','environment_url':'https://cellestial.ru'}]
        api.pages=pages
        self.assertFalse(api.record(raw)['trusted'])
    def test_skipped_health_even_with_trusted_status_is_not_success(self):
        fixture=self.fixture();raw=fixture['deployment'];api=FakeGitHub()
        api.get=lambda path: {'id':1} if '/workflows/' in path else fixture['run']
        api.pages=lambda path,field=None: [{'name':'deploy / deploy','steps':[{'name':'Health check','conclusion':'skipped'}]}] if '/jobs' in path else [{'state':'success','creator':{'login':'github-actions[bot]'},'log_url':r.RUN_ROOT+'20/attempts/1','environment_url':'https://cellestial.ru'}]
        self.assertFalse(api.record(raw)['trusted'])
    def test_expired_old_run_retains_unknown_barrier_not_global_failure(self):
        fixture=self.fixture();api=FakeGitHub([fixture['previous']])
        # Классификация ошибок настоящего адаптера проверяется и через HTTPError.
        class Missing:
            def open(self,*args,**kwargs):
                import urllib.error
                raise urllib.error.HTTPError('https://api.github.com/expired',404,'missing',{},None)
        transport=r.GitHub('test',Missing())
        api.get=transport.get
        history=api.history()
        self.assertFalse(history[0]['trusted'])
        with self.assertRaises(r.ReleaseError):r.select_target(history)

class ExistingOperationIdentity(unittest.TestCase):
    def test_foreign_deployment_cannot_claim_the_current_operation_key(self):
        s=r.initial_state('deploy',10,1);s.update(target_sha=A,image_ref=D)
        record={'id':3,'sha':A,'ref':A,'environment':'production','creator':{'login':'other'},'payload':{'run':r.RUN_ROOT+'10','action':'deploy','image_ref':D,'notification':{'run_attempt':1}}}
        with self.assertRaises(r.ReleaseError):FakeGitHub([record]).ensure_deployment(s)
