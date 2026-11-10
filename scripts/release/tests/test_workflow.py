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
                     'GH_TOKEN':'test','GITHUB_STEP_SUMMARY':d+'/summary','GITHUB_OUTPUT':d+'/outputs'}
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
                         PRODUCTION_ENABLED='true',GITHUB_EVENT_NAME='push',SOURCE_WORKFLOW=r.REPOSITORY+'/.github/workflows/ci.yml@refs/heads/main',
                         GITHUB_RUN_ID='10',GITHUB_RUN_ATTEMPT='1',GITHUB_STEP_SUMMARY=d+'/summary',GITHUB_ENV=d+'/env',RUNNER_TEMP=d)
                env.update(change)
                result=subprocess.run(['bash','-e','-c',command],env=env,capture_output=True)
                self.assertEqual(result.returncode==0,not bool(change))
                self.assertIn('состояние VPS не проверено',Path(d+'/summary').read_text())
