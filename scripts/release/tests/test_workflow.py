"""Exercise actual workflow source gate and CLI refusal without network/VPS."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import sys
sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_release import r, FakeGitHub, A, D

ROOT = Path(__file__).resolve().parents[3]

class Workflow(unittest.TestCase):
    def test_invalid_target_and_history_failure_never_call_docker_or_ssh(self):
        for target, history_error in [('main', False), (A, True)]:
            with self.subTest(target=target, history_error=history_error), tempfile.TemporaryDirectory() as d:
                env={'RELEASE_STATE_FILE':d+'/state.json','RELEASE_ACTION':'rollback','RELEASE_TARGET_SHA':target,
                     'GITHUB_RUN_ID':'10','GITHUB_RUN_ATTEMPT':'1','GITHUB_REPOSITORY':r.REPOSITORY,
                     'GITHUB_REF':'refs/heads/main','GITHUB_EVENT_NAME':'workflow_dispatch','PRODUCTION_ENABLED':'true',
                     'GITHUB_WORKFLOW_REF':r.REPOSITORY+'/.github/workflows/rollback.yml@refs/heads/main','GH_TOKEN':'test','GITHUB_STEP_SUMMARY':d+'/summary','GITHUB_OUTPUT':d+'/outputs'}
                api=FakeGitHub();api.page_failure=history_error
                with patch.dict(os.environ,env,clear=True),patch.object(r,'GitHub',return_value=api),patch.object(r.subprocess,'run') as process:
                    self.assertEqual(r.main(['init']),0)
                    self.assertEqual(r.main(['prepare']),1)
                    self.assertEqual(r.main(['report']),0)
                    self.assertIn('История изменений неполная',Path(d+'/summary').read_text())
                    process.assert_not_called();self.assertEqual(api.posts,[])

    def test_rerun_does_not_read_previous_attempt_state(self):
        with tempfile.TemporaryDirectory() as d:
            r.save(d+'/state',r.initial_state('deploy',10,1))
            with patch.dict(os.environ,{'RELEASE_STATE_FILE':d+'/state','GITHUB_RUN_ID':'10','GITHUB_RUN_ATTEMPT':'2'}):
                with self.assertRaises(r.ReleaseError):r.main(['health'])

    def test_actual_gate_rejects_branch_repository_and_disabled_flag(self):
        import textwrap
        source=(ROOT/'.github/workflows/cd.yml').read_text()
        command=textwrap.dedent(source.split('        run: |\n',1)[1].split('      - name:',1)[0])
        for change in [{},{'GITHUB_REF':'refs/heads/topic'},{'PRODUCTION_ENABLED':'false'},{'GITHUB_REPOSITORY':'other/backend'}]:
            with tempfile.TemporaryDirectory() as d:
                env=dict(os.environ,RELEASE_ACTION='deploy',GITHUB_REPOSITORY=r.REPOSITORY,GITHUB_REF='refs/heads/main',
                         PRODUCTION_ENABLED='true',GITHUB_EVENT_NAME='workflow_dispatch',SOURCE_WORKFLOW=r.REPOSITORY+'/.github/workflows/cd.yml@refs/heads/main',
                         GITHUB_RUN_ID='10',GITHUB_RUN_ATTEMPT='1',GITHUB_STEP_SUMMARY=d+'/summary',GITHUB_ENV=d+'/env',RUNNER_TEMP=d)
                env.update(change)
                result=subprocess.run(['bash','-e','-c',command],env=env,capture_output=True)
                self.assertEqual(result.returncode==0,not bool(change))
                self.assertIn('состояние VPS не проверено',Path(d+'/summary').read_text())

    def test_cd_uses_manifest_not_dispatch_sha_and_repeats_current_release(self):
        import artifacts
        for base, target, comparison, expected in [(A,A,'identical',0),(A,'b'*40,'ahead',0),(A,'b'*40,'behind',1)]:
            with self.subTest(comparison=comparison), tempfile.TemporaryDirectory() as d:
                env={'RELEASE_STATE_FILE':d+'/state','RELEASE_ACTION':'deploy','RELEASE_TAG':'release-'+target,
                     'GITHUB_RUN_ID':'10','GITHUB_RUN_ATTEMPT':'1','GITHUB_REPOSITORY':r.REPOSITORY,
                     'GITHUB_REF':'refs/heads/main','GITHUB_EVENT_NAME':'workflow_dispatch','PRODUCTION_ENABLED':'true',
                     'GITHUB_WORKFLOW_REF':r.REPOSITORY+'/.github/workflows/cd.yml@refs/heads/main',
                     'GITHUB_SHA':'c'*40,'GH_TOKEN':'test','GITHUB_OUTPUT':d+'/outputs'}
                api=FakeGitHub();api.history=lambda:[{'sha':base,'image_ref':D,'state':'success','trusted':True}]
                api.get=lambda _: {'status':comparison}
                with patch.dict(os.environ,env,clear=True),patch.object(r,'GitHub',return_value=api),patch.object(artifacts,'resolve',return_value={'image_ref':D}) as resolve,patch.object(r,'check_image') as pull:
                    self.assertEqual(r.main(['init']),0)
                    self.assertEqual(r.main(['prepare']),expected)
                    if expected==0:
                        state=json.loads(Path(d+'/state').read_text())
                        self.assertEqual(state['target_sha'],target);self.assertEqual(state['image_ref'],D)
                        resolve.assert_called_once_with(api,'release-'+target);pull.assert_called_once_with(D)

    def test_rollback_refuses_release_digest_different_from_confirmed_history(self):
        import artifacts
        with tempfile.TemporaryDirectory() as d:
            env={'RELEASE_STATE_FILE':d+'/state','RELEASE_ACTION':'rollback','RELEASE_TARGET_SHA':A,
                 'GITHUB_RUN_ID':'10','GITHUB_RUN_ATTEMPT':'1','GITHUB_REPOSITORY':r.REPOSITORY,
                 'GITHUB_REF':'refs/heads/main','GITHUB_EVENT_NAME':'workflow_dispatch','PRODUCTION_ENABLED':'true',
                 'GITHUB_WORKFLOW_REF':r.REPOSITORY+'/.github/workflows/rollback.yml@refs/heads/main','GH_TOKEN':'test'}
            api=FakeGitHub();api.history=lambda:[{'sha':A,'image_ref':D,'state':'success','trusted':True}]
            with patch.dict(os.environ,env,clear=True),patch.object(r,'GitHub',return_value=api),patch.object(artifacts,'resolve',return_value={'image_ref':D[:-1]+'e'}),patch.object(r.subprocess,'run') as process:
                self.assertEqual(r.main(['init']),0);self.assertEqual(r.main(['prepare']),1)
                process.assert_not_called();self.assertEqual(api.posts,[])

    def test_repeated_switch_always_runs_ansible_with_unchanged_digest(self):
        with tempfile.TemporaryDirectory() as d:
            env={'RELEASE_STATE_FILE':d+'/state','RELEASE_ACTION':'deploy','GITHUB_RUN_ID':'10','GITHUB_RUN_ATTEMPT':'1',
                 'GITHUB_REPOSITORY':r.REPOSITORY,'GITHUB_REF':'refs/heads/main','GITHUB_EVENT_NAME':'workflow_dispatch',
                 'PRODUCTION_ENABLED':'true','GITHUB_WORKFLOW_REF':r.REPOSITORY+'/.github/workflows/cd.yml@refs/heads/main',
                 'ANSIBLE_DIR':d,'DEPLOY_KEY_PATH':d+'/key','JUMP_KNOWN_HOSTS':d+'/known'}
            state=r.initial_state('deploy',10,1);state.update(target_sha=A,image_ref=D,started=True)
            def switch(command,**kwargs):
                self.assertIn('deploy-backend.yml',command)
                extra=json.loads(Path(command[command.index('-e')+1][1:]).read_text())
                self.assertEqual(extra['backend_image_ref'],D)
                current=json.loads(Path(d+'/state').read_text());current['checks']['switch']='passed';r.save(d+'/state',current)
                return subprocess.CompletedProcess(command,0)
            r.save(d+'/state',state)
            with patch.dict(os.environ,env,clear=True),patch.object(r.subprocess,'check_output',side_effect=['b'*40,'c'*40]),patch.object(r.subprocess,'run',side_effect=switch) as process:
                self.assertEqual(r.main(['switch']),0);self.assertEqual(r.main(['switch']),0)
                self.assertEqual(process.call_count,2)
                final=json.loads(Path(d+'/state').read_text());self.assertEqual(final['infra_sha'],'c'*40)
                self.assertEqual(final['image_ref'],D)

    def test_dispatch_failure_preserves_ready_release_and_never_claims_deployment_success(self):
        import textwrap
        source=(ROOT/'.github/workflows/ci.yml').read_text()
        command=textwrap.dedent(source.split('      - name: Dispatch prepared Release to CD\n',1)[1].split('        run: |\n',1)[1].split('\n  course-sync:',1)[0])
        for code in (0,1):
            with tempfile.TemporaryDirectory() as d:
                env=dict(os.environ,GITHUB_STEP_SUMMARY=d+'/summary',GITHUB_REPOSITORY=r.REPOSITORY,RELEASE_TAG='release-'+A)
                script='gh() { return '+str(code)+'; }\n'+command
                result=subprocess.run(['bash','-e','-c',script],env=env,capture_output=True)
                self.assertEqual(result.returncode,code)
                report=Path(d+'/summary').read_text()
                self.assertIn('release-'+A,report)
                if code:
                    self.assertIn('remains prepared',report);self.assertIn('Run CD manually',report)
                else: self.assertIn('Follow the separate CD run',report)
