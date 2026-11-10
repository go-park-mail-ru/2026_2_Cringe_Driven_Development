"""Optional real callback loading test, using localhost asserts, no SSH or registry."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_release import r

class RealCallback(unittest.TestCase):
    @unittest.skipUnless(shutil.which('ansible-playbook'), 'ansible-playbook required for callback loading test')
    def test_ansible_loads_callback_and_preserves_partial_health(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);tasks=root/'roles/app/tasks';tasks.mkdir(parents=True)
            state=root/'state.json';r.save(state,r.initial_state('deploy',10,1))
            (tasks/'check-backend-image.yml').write_text('- name: Backend image check\n  ansible.builtin.assert:\n    that: true\n')
            (tasks/'probe-backend.yml').write_text('- name: Backend internal HTTP check\n  ansible.builtin.assert:\n    that: true\n')
            (tasks/'probe-bff.yml').write_text('- name: Backend external BFF check\n  ansible.builtin.assert:\n    that: false\n')
            play=root/'play.yml';play.write_text('''- hosts: localhost
  connection: local
  gather_facts: false
  tasks:
    - ansible.builtin.import_tasks: roles/app/tasks/check-backend-image.yml
    - ansible.builtin.import_tasks: roles/app/tasks/probe-backend.yml
    - ansible.builtin.import_tasks: roles/app/tasks/probe-bff.yml
''')
            env=dict(os.environ,ANSIBLE_LOCAL_TEMP=str(root/'local'),ANSIBLE_REMOTE_TEMP=str(root/'remote'),
                     ANSIBLE_CALLBACK_PLUGINS=str(Path(__file__).resolve().parents[1]/'callback_plugins'),ANSIBLE_CALLBACKS_ENABLED='backend_result',
                     RELEASE_STATE_FILE=str(state),GITHUB_RUN_ID='10',GITHUB_RUN_ATTEMPT='1',ANSIBLE_NOCOLOR='1')
            result=subprocess.run(['ansible-playbook','-i','localhost,',str(play)],env=env,capture_output=True,timeout=30)
            self.assertNotEqual(result.returncode,0)
            actual=json.loads(state.read_text())
            self.assertEqual(actual['checks']['switch'],'passed',result.stdout.decode()+result.stderr.decode())
            self.assertEqual(actual['checks']['internal'],'passed')
            self.assertEqual(actual['checks']['external'],'failed')
            self.assertEqual(actual['checks']['routes'],'not_run')
