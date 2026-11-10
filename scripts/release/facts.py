"""Names/paths of the actual infra#69 checks; unknown tasks cannot confirm health."""
import re


def observe(state, path, name, result, data):
    checks = state['checks']
    path = path.rsplit(':', 1)[0].replace('\\', '/')
    if path.endswith('roles/app/tasks/check-backend-image.yml') and result == 'ok' and not data.get('_ansible_no_log'):
        if name == 'Read the backend container image and health':
            fields = data.get('stdout', '').split()
            if len(fields) == 3 and re.fullmatch(r'sha256:[0-9a-f]{64}', fields[0]):
                state['running_image_id'] = fields[0] if fields[1] == 'true' else None
        elif name == 'Read the local ID of the selected backend digest':
            image_id = data.get('stdout', '').strip()
            if re.fullmatch(r'sha256:[0-9a-f]{64}', image_id):
                state['selected_image_id'] = image_id
        if state.get('running_image_id') and state.get('running_image_id') == state.get('selected_image_id'):
            checks['switch'] = 'passed'
    stages = (
        ('check-backend-image.yml', 'Backend image check', 'image'),
        ('probe-backend.yml', 'Backend internal HTTP check', 'internal'),
        ('probe-bff.yml', 'Backend external BFF check', 'external'),
        ('probe-bff.yml', 'Go API, unknown API paths and the internal health are not public', 'routes'),
    )
    for file, expected, stage in stages:
        if not path.endswith('/roles/app/tasks/' + file) and path != 'roles/app/tasks/' + file:
            continue
        if name != expected:
            continue
        value = {'ok': 'passed', 'failed': 'failed', 'unreachable': 'unknown', 'skipped': 'not_run'}[result]
        if data.get('_ansible_no_log') or data.get('failed') or data.get('skipped'):
            value = 'unknown'
        if stage == 'routes' and value == 'passed':
            rows = data.get('results', [])
            if ({row.get('item') for row in rows} != {'/api/v1/users/me', '/api/unknown', '/health'}
                    or len(rows) != 3 or any(row.get('failed') or row.get('skipped') for row in rows)):
                value = 'unknown'
        checks[stage] = value
        if stage == 'image' and value == 'passed':
            checks['switch'] = 'passed'
        if value in ('failed', 'unknown'):
            state['error_stage'] = stage
    if result in ('failed', 'unreachable') and not state.get('error_stage'):
        state['error_stage'] = 'ansible'

    if all(checks.get(k) == 'passed' for k in ('switch','image','internal','external','routes')):
        state['mandatory_complete'] = True
