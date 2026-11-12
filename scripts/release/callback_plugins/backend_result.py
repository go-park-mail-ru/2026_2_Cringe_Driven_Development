"""Write only allowlisted facts; never Ansible result bodies or secrets."""
import importlib.util
import json
import os
from pathlib import Path
from ansible.plugins.callback import CallbackBase

_ROOT = Path(__file__).resolve().parents[1]

def load(name, file):
    spec = importlib.util.spec_from_file_location(name, _ROOT / file)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

facts = load('backend_release_facts', 'facts.py')
release = load('backend_release_state', 'release.py')


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = 'aggregate'
    CALLBACK_NAME = 'backend_result'
    CALLBACK_NEEDS_ENABLED = True

    def observe(self, result, outcome):
        path = Path(os.environ['RELEASE_STATE_FILE'])
        state = json.loads(path.read_text())
        if state['run_id'] != int(os.environ['GITHUB_RUN_ID']) or state['run_attempt'] != int(os.environ['GITHUB_RUN_ATTEMPT']):
            raise RuntimeError('Result belongs to another operation')
        facts.observe(state, result._task.get_path(), result._task.get_name().split(' : ')[-1], outcome, result._result)
        release.save(path, state)

    def v2_runner_on_ok(self, result):
        self.observe(result, 'ok')

    def v2_runner_on_failed(self, result, ignore_errors=False):
        self.observe(result, 'failed')

    def v2_runner_on_unreachable(self, result):
        self.observe(result, 'unreachable')

    def v2_runner_on_skipped(self, result):
        self.observe(result, 'skipped')
