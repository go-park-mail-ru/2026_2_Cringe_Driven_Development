#!/usr/bin/env python3
"""Backend release producer. GitHub is history; Ansible supplies observed facts."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

REPOSITORY = 'Cringe-Driven-Development-Team/backend'
PACKAGE = 'ghcr.io/cringe-driven-development-team/backend'
RUN_ROOT = f'https://github.com/{REPOSITORY}/actions/runs/'
if __name__ == '__main__':
    sys.modules['release'] = sys.modules[__name__]
CHECKS = ('switch', 'image', 'internal', 'external', 'routes')


class ReleaseError(Exception):
    """Safe diagnostic without credentials or upstream response bodies."""


class HistoricalRunMissing(ReleaseError):
    """A deleted historical run is unknown, rather than an outage of the API."""


def validate_sha(value):
    if not isinstance(value, str) or not re.fullmatch('[0-9a-fA-F]{40}', value):
        raise ReleaseError('Expected a full 40-hex target SHA')
    return value.lower()


def validate_image(value):
    if not isinstance(value, str) or not re.fullmatch(re.escape(PACKAGE) + r'@sha256:[0-9a-f]{64}', value):
        raise ReleaseError('Expected the backend package with a full sha256 digest')
    return value


def select_target(history, explicit=''):
    successful = [x for x in history if x['trusted'] and x['state'] == 'success']
    if explicit:
        target = validate_sha(explicit)
        candidates = [x for x in successful if x['sha'] == target]
    else:
        if not history or not history[0]['trusted']:
            raise ReleaseError('No trusted latest production attempt; specify a confirmed SHA')
        latest = history[0]
        if latest['state'] == 'success':
            candidates = [x for x in successful[1:] if x['image_ref'] != latest['image_ref']]
        elif latest['state'] in ('failure', 'error'):
            candidates = successful
        else:
            raise ReleaseError('Latest production attempt is unfinished or unknown; specify a confirmed SHA')
    if not candidates:
        raise ReleaseError('No suitable successful production Deployment')
    chosen = candidates[0]
    refs = {x['image_ref'] for x in successful if x['sha'] == chosen['sha']}
    if len(refs) != 1:
        raise ReleaseError('Successful Deployments contain different digests for the target SHA')
    return validate_sha(chosen['sha']), validate_image(chosen['image_ref'])


def initial_state(action, run_id, attempt):
    if action not in ('deploy', 'rollback') or int(run_id) <= 0 or int(attempt) <= 0:
        raise ReleaseError('Invalid operation identity')
    return {'action': action, 'run_id': int(run_id), 'run_attempt': int(attempt),
            'target_sha': None, 'image_ref': None, 'base_sha': None, 'deployment_id': None,
            'checks': {x: 'not_run' for x in CHECKS}, 'ansible_rc': None, 'error_stage': None,
            'requested_sha': os.environ.get('RELEASE_TARGET_SHA', ''),
            'requested_tag': os.environ.get('RELEASE_TAG', ''),
            'actor': os.environ.get('GITHUB_TRIGGERING_ACTOR') or os.environ.get('GITHUB_ACTOR', 'unknown')}


def outcome(state):
    if (state['ansible_rc'] == 0 or state.get('mandatory_complete')) and all(state['checks'].get(x) == 'passed' for x in CHECKS):
        return 'success'
    if any(value == 'failed' for value in state['checks'].values()):
        return 'failure'
    return 'error'


def save(path, state):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, delete=False) as f:
        json.dump(state, f)
        name = f.name
    os.replace(name, path)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ReleaseError('GitHub redirect refused')


class GitHub:
    def __init__(self, token, opener=None):
        if not token:
            raise ReleaseError('Missing GitHub token')
        self.token = token
        self.opener = opener or urllib.request.build_opener(NoRedirect())
        self.prefix = f'/repos/{REPOSITORY}'

    def request(self, path, body=None, method=None):
        url = urllib.parse.urljoin('https://api.github.com', path)
        u = urllib.parse.urlsplit(url)
        if u.scheme != 'https' or u.netloc != 'api.github.com' or u.username or u.password:
            raise ReleaseError('Untrusted GitHub URL')
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(url, data=data, method=method, headers={
            'Authorization': f'Bearer {self.token}', 'Accept': 'application/vnd.github+json',
            'X-GitHub-Api-Version': '2022-11-28', 'Content-Type': 'application/json'})
        try:
            with self.opener.open(req, timeout=15) as response:
                result = json.load(response)
                link = response.headers.get('Link', '')
                match = re.search(r'<([^>]+)>;\s*rel="next"', link)
                return result, match[1] if match else None
        except urllib.error.HTTPError as error:
            if error.code == 404:
                raise HistoricalRunMissing('GitHub historical resource missing') from None
            raise ReleaseError(f'GitHub HTTP {error.code}') from None
        except (OSError, ValueError):
            raise ReleaseError('GitHub unavailable or invalid JSON') from None

    def get(self, path):
        return self.request(self.prefix + path)[0]

    def pages(self, path, field=None):
        url = self.prefix + path
        expected = urllib.parse.urlsplit(url).path
        seen, rows = set(), []
        while url:
            absolute = urllib.parse.urljoin('https://api.github.com', url)
            if absolute in seen or urllib.parse.urlsplit(absolute).path != expected:
                raise ReleaseError('Invalid GitHub pagination')
            seen.add(absolute)
            body, url = self.request(absolute)
            page = body[field] if field else body
            if not isinstance(page, list):
                raise ReleaseError('Invalid GitHub list')
            rows.extend(page)
        return rows

    def record(self, raw):
        """Keep unknown latest records in history: never silently skip them for selection."""
        row = {'id': raw.get('id', 0), 'sha': raw.get('sha'), 'image_ref': None,
               'state': 'unknown', 'trusted': False}
        payload = raw.get('payload')
        if not isinstance(payload, dict):
            return row
        n = payload.get('notification', {})
        if not isinstance(n, dict) or 'base_sha' not in n:
            return row
        action = payload.get('action')
        try:
            target = validate_sha(raw.get('sha'))
            ref = validate_image(payload.get('image_ref'))
        except ReleaseError:
            return row
        run_url = payload.get('run', '')
        match = re.fullmatch(re.escape(RUN_ROOT) + r'([1-9][0-9]*)', run_url)
        attempt = n.get('run_attempt')
        if (raw.get('environment') != 'production' or raw.get('task') != action
                or action not in ('deploy', 'rollback') or raw.get('ref') != target
                or raw.get('creator', {}).get('login') != 'github-actions[bot]'
                or not match or type(attempt) is not int or attempt < 1
                or n.get('report_url') != run_url + '#summary'
                or (n.get('base_sha') is not None and not re.fullmatch('[0-9a-f]{40}', str(n['base_sha'])))):
            return row
        run_id = int(match[1])
        run = self.get(f'/actions/runs/{run_id}/attempts/{attempt}')
        historical_ci = action == 'deploy' and run.get('path') == '.github/workflows/ci.yml'
        workflow = ('ci.yml' if historical_ci else 'cd.yml') if action == 'deploy' else 'rollback.yml'
        workflow_id = self.get(f'/actions/workflows/{workflow}')['id']
        if (run.get('id') != run_id or run.get('run_attempt') != attempt
                or run.get('path') != f'.github/workflows/{workflow}'
                or run.get('workflow_id') != workflow_id
                or run.get('head_branch') != 'main'
                or run.get('event') != ('push' if historical_ci else 'workflow_dispatch')
                or (historical_ci and run.get('head_sha') != target)
                or run.get('repository', {}).get('full_name') != REPOSITORY
                or run.get('head_repository', {}).get('full_name') != REPOSITORY):
            return row
        jobs = self.pages(f'/actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100', 'jobs')
        expected_job = ('deploy / deploy' if historical_ci else 'deploy') if action == 'deploy' else 'rollback / deploy'
        candidates = [j for j in jobs if j.get('name') == expected_job]
        if len(candidates) != 1:
            return row
        statuses = self.pages(f'/deployments/{raw["id"]}/statuses?per_page=100')
        relevant = next((s for s in statuses if s.get('state') != 'inactive'), None)
        current = relevant.get('state') if relevant else 'pending'
        if relevant and (relevant.get('creator', {}).get('login') != 'github-actions[bot]'
                         or relevant.get('log_url') != run_url + f'/attempts/{attempt}'
                         or relevant.get('environment_url') != 'https://cellestial.ru'):
            return row
        if current == 'success':
            steps = candidates[0].get('steps') or []
            for name in ('Switch backend', 'Health check'):
                matches = [s for s in steps if s.get('name') == name]
                if len(matches) != 1 or matches[0].get('conclusion') != 'success':
                    return row
        row.update(sha=target, image_ref=ref, state=current, trusted=True,
                   action=action, run_id=run_id, run_attempt=attempt)
        return row

    def history(self):
        raw = self.pages('/deployments?environment=production&per_page=100')
        raw.sort(key=lambda d: (d.get('created_at', ''), d['id']), reverse=True)
        records = []
        for d in raw:
            try:
                records.append(self.record(d))
            except HistoricalRunMissing:
                records.append({'id': d['id'], 'sha': d.get('sha'), 'image_ref': None, 'state': 'unknown', 'trusted': False})
        return records

    def ensure_deployment(self, state):
        # У перезапуска новая попытка; повторная финализация никогда не создаёт Deployment.
        def existing():
            matches = []
            for d in self.pages('/deployments?environment=production&per_page=100'):
                p = d.get('payload') or {}
                if isinstance(p, dict) and p.get('run') == RUN_ROOT + str(state['run_id']) and p.get('action') == state['action'] and p.get('notification', {}).get('run_attempt') == state['run_attempt']:
                    if (d.get('sha') != state['target_sha'] or d.get('ref') != state['target_sha']
                            or d.get('environment') != 'production' or d.get('creator', {}).get('login') != 'github-actions[bot]'
                            or p.get('image_ref') != state['image_ref']):
                        raise ReleaseError('Conflicting Deployment for operation key')
                    matches.append(d['id'])
            if len(matches) > 1:
                raise ReleaseError('Duplicate Deployments for operation key')
            return matches[0] if matches else None
        found = existing()
        if found:
            return found
        run = RUN_ROOT + str(state['run_id'])
        body = {'ref': state['target_sha'], 'task': state['action'], 'environment': 'production',
                'auto_merge': False, 'required_contexts': [], 'production_environment': True,
                'payload': {'action': state['action'], 'image_ref': state['image_ref'], 'run': run,
                            'release_tag': state.get('release_tag'), 'infra_sha': state.get('infra_sha'),
                            'notification': {'run_attempt': state['run_attempt'], 'base_sha': state['base_sha'], 'report_url': run + '#summary'}}}
        try:
            d, _ = self.request(self.prefix + '/deployments', body)
        except ReleaseError:
            found = existing()
            if found:
                return found
            raise ReleaseError('Deployment create not confirmed; not retrying POST') from None
        if d.get('sha') != state['target_sha'] or not isinstance(d.get('id'), int):
            raise ReleaseError('Deployment resolved to an unexpected SHA')
        return d['id']

    def status(self, state, status):
        self.request(self.prefix + f'/deployments/{state["deployment_id"]}/statuses', {
            'state': status, 'environment': 'production', 'environment_url': 'https://cellestial.ru',
            'log_url': RUN_ROOT + str(state['run_id']) + f'/attempts/{state["run_attempt"]}',
            'auto_inactive': False, 'description': 'All mandatory checks passed' if status == 'success' else f'{state.get("error_stage") or status}: see run Summary'})


def check_image(ref):
    validate_image(ref)
    with tempfile.TemporaryDirectory(prefix='backend-anonymous-') as config:
        # Отдельный конфиг без хелперов, учётных данных login-action и унаследованной аутентификации.
        subprocess.run(['docker', '--config', config, 'pull', '--platform', 'linux/amd64', ref], check=True, timeout=300)


def md(value):
    return str(value).replace('&', '&amp;').replace('<', '&lt;').replace('>', '&gt;').replace('\n', ' ').replace('|', '\\|')


def summary(state, api=None):
    run = RUN_ROOT + str(state['run_id'])
    lines = ['# Backend production: ' + state['action'], '',
             f'- Запрошенный Release: {md(state.get("requested_tag") or "не указан")}',
             f'- Запрошенный SHA: {md(state.get("requested_sha") or "автоматический выбор")}',
             f'- Release: `{state.get("release_tag") or "не определён"}`',
             f'- Infra SHA: `{state.get("infra_sha") or os.environ.get("RELEASE_INFRA_SHA") or "не определён"}`',
             f'- Target SHA: `{state["target_sha"] or "не определён"}`',
             f'- Image ref: `{state["image_ref"] or "не определён"}`',
             f'- Последний подтверждённый релиз GitHub: `{state["base_sha"] or "нет истории"}` (не текущее состояние VPS)',
             '- Инициатор: ' + md(state['actor']),
             f'- Run/attempt: [{state["run_id"]}/{state["run_attempt"]}]({run}/attempts/{state["run_attempt"]})',
             '- Результат операции: ' + outcome(state),
             '- Этап ошибки: ' + md(state.get('error_stage') or 'нет'),
             '- Deployment ID: ' + str(state.get('deployment_id') or 'не создан'),
             '- Записанный terminal status: ' + str(state.get('terminal_status') or 'не подтверждён'),
             '- Ошибка регистрации результата: ' + str(state.get('registration_error') or 'нет'),
             '- Состояние: ' + ('проверено' if outcome(state) == 'success' else 'выбранная версия запущена на этапе переключения; итоговое состояние неизвестно' if state['checks']['switch'] == 'passed' else 'неизвестно; подтверждённые этапы ниже'),
             '', '| Проверка | Результат |', '| --- | --- |']
    labels = {'switch': 'Switch backend', 'image': 'Выбранный образ и healthy контейнер', 'internal': 'Внутренний Go с gateway', 'external': 'Внешний BFF → Go → PostgreSQL', 'routes': 'Закрытость публичных Go-маршрутов'}
    lines.extend(f'| {labels[k]} | {state["checks"][k]} |' for k in CHECKS)
    lines += ['', 'Health Check: ' + ('✅' if outcome(state) == 'success' else '❌' if outcome(state) == 'failure' else '⚪'),
              'Не проверяет успешную пользовательскую сессию, Redis или runtime-VPS.', '',
              '[backend#42](https://github.com/' + REPOSITORY + '/issues/42) · [infra#69](https://github.com/Cringe-Driven-Development-Team/infra/issues/69) · [.github#59](https://github.com/Cringe-Driven-Development-Team/.github/issues/59)',
              '', '## ' + ('Снимаемые изменения' if state['action'] == 'rollback' else 'Изменения')]
    try:
        base, target = state['base_sha'], state['target_sha']
        if not api or not base or not target:
            raise ReleaseError('No confirmed comparison base')
        start, end = (target, base) if state['action'] == 'rollback' else (base, target)
        lines.append(f'[Сравнение](https://github.com/{REPOSITORY}/compare/{start}...{end})')
        page, seen, total = 1, set(), None
        while True:
            data = api.get(f'/compare/{start}...{end}?per_page=100&page={page}')
            if data.get('status') not in ('ahead', 'identical'):
                raise ReleaseError('Divergent history')
            total = data['total_commits']
            commits = data['commits']
            fresh = [c for c in commits if c['sha'] not in seen]
            if not fresh and len(seen) != total:
                raise ReleaseError('Incomplete comparison')
            for c in fresh:
                sha = validate_sha(c['sha']); seen.add(sha)
                lines.append(f'- [{sha[:7]}](https://github.com/{REPOSITORY}/commit/{sha}) {md(c["commit"]["message"].splitlines()[0])}')
                for pr in api.pages(f'/commits/{sha}/pulls?per_page=100'):
                    if pr.get('merged_at'):
                        number = int(pr['number'])
                        lines.append(f'  - [PR #{number}](https://github.com/{REPOSITORY}/pull/{number}) {md(pr["title"])}')
                        # Сохраняем явные ссылки на задачи, не придумывая принадлежность задаче.
                        body = pr.get('body') or ''
                        for issue in sorted(set(re.findall(r'(?i)(?:closes|fixes|resolves|part of)\s+#([1-9][0-9]*)', body))):
                            lines.append(f'  - [Задача #{issue}](https://github.com/{REPOSITORY}/issues/{issue})')
            if len(seen) == total:
                break
            if len(seen) > total:
                raise ReleaseError('Inconsistent comparison')
            page += 1
    except (ReleaseError, KeyError, TypeError, ValueError):
        lines.append('**История изменений неполная. Результат операции указан независимо от enrichment.**')
    return '\n'.join(lines) + '\n'


def source_guard(action):
    if (os.environ.get('GITHUB_REPOSITORY') != REPOSITORY or os.environ.get('GITHUB_REF') != 'refs/heads/main'
            or os.environ.get('PRODUCTION_ENABLED') != 'true'
            or os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch'):
        raise ReleaseError('Production requires the trusted repository, main and PRODUCTION_ENABLED=true')
    workflow = 'cd.yml' if action == 'deploy' else 'rollback.yml'
    if os.environ.get('GITHUB_WORKFLOW_REF') != REPOSITORY + '/.github/workflows/' + workflow + '@refs/heads/main':
        raise ReleaseError('Untrusted production workflow')


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=('init', 'prepare', 'begin', 'switch', 'health', 'finalize', 'report'))
    args = parser.parse_args(argv)
    path = Path(os.environ['RELEASE_STATE_FILE'])
    api = None
    if args.command == 'init':
        save(path, initial_state(os.environ['RELEASE_ACTION'], os.environ['GITHUB_RUN_ID'], os.environ['GITHUB_RUN_ATTEMPT']))
        return 0
    state = json.loads(path.read_text())
    if state['run_id'] != int(os.environ['GITHUB_RUN_ID']) or state['run_attempt'] != int(os.environ['GITHUB_RUN_ATTEMPT']):
        raise ReleaseError('State belongs to another run/attempt')
    try:
        if args.command in ('prepare', 'begin', 'finalize'):
            api = GitHub(os.environ.get('GH_TOKEN', ''))
        elif args.command == 'report':
            api = GitHub(os.environ.get('GH_TOKEN', '')) if os.environ.get('GH_TOKEN') else None
        if args.command == 'prepare':
            source_guard(state['action'])
            history = api.history()
            successful = next((x for x in history if x['trusted'] and x['state'] == 'success'), None)
            state['base_sha'] = successful['sha'] if successful else None
            if state['action'] == 'rollback':
                if os.environ.get('RELEASE_IMAGE_REF'):
                    raise ReleaseError('Rollback digest must come from Deployments')
                target, image = select_target(history, os.environ.get('RELEASE_TARGET_SHA', ''))
            else:
                from artifacts import tag_sha
                target = tag_sha(os.environ.get('RELEASE_TAG', ''))
                image = None
            from artifacts import resolve, forward_guard
            tag = 'release-' + target
            manifest = resolve(api, tag)
            if image and image != manifest['image_ref']:
                raise ReleaseError('Deployment digest differs from immutable Release')
            image = manifest['image_ref']
            if state['action'] == 'deploy':
                if history and (not history[0]['trusted'] or history[0]['state'] not in ('success', 'failure', 'error')):
                    raise ReleaseError('Latest production state unknown; reconcile before CD')
                forward_guard(state['base_sha'], target, lambda base, goal: api.get(f'/compare/{base}...{goal}').get('status'))
            state['release_tag'] = tag
            state.update(target_sha=target, image_ref=image)
            save(path, state)
            check_image(image)
            with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
                output.write(f'target_sha={target}\nimage_ref={image}\n')
        elif args.command == 'begin':
            source_guard(state['action'])
            validate_sha(state['target_sha']); validate_image(state['image_ref'])
            state['infra_sha'] = validate_sha(os.environ['RELEASE_INFRA_SHA'])
            state['deployment_id'] = api.ensure_deployment(state)
            save(path, state)
            api.status(state, 'in_progress')
            state['started'] = True
        elif args.command == 'switch':
            if not state.get('started'):
                raise ReleaseError('Deployment in_progress not confirmed')
            source_guard(state['action'])
            state['checks']['switch'] = 'unknown'; save(path, state)
            state['infra_sha'] = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=os.environ['ANSIBLE_DIR'], text=True).strip()
            save(path, state)
            env = dict(os.environ)
            env['ANSIBLE_CALLBACK_PLUGINS'] = str(Path(__file__).parent / 'callback_plugins')
            env['ANSIBLE_CALLBACKS_ENABLED'] = 'backend_result'
            extra = {'backend_image_ref': state['image_ref'], 'deploy_key_path': os.environ['DEPLOY_KEY_PATH'], 'jump_known_hosts': os.environ['JUMP_KNOWN_HOSTS']}
            with tempfile.NamedTemporaryFile(mode='w', suffix='.json') as f:
                json.dump(extra, f); f.flush()
                command = ['ansible-playbook', '-i', 'inventory/ci.yml', 'deploy-backend.yml', '-e', '@' + f.name,
                           '--ssh-common-args=-o UserKnownHostsFile=' + os.environ['JUMP_KNOWN_HOSTS'] + ' -o StrictHostKeyChecking=yes']
                completed = subprocess.run(command, cwd=os.environ['ANSIBLE_DIR'], env=env, check=False)
            state = json.loads(path.read_text())
            state['ansible_rc'] = completed.returncode
            save(path, state)
            if state['checks']['switch'] != 'passed':
                raise ReleaseError('Backend switch not confirmed; see Ansible logs')
        elif args.command == 'health':
            if outcome(state) != 'success':
                raise ReleaseError('Not all mandatory production checks passed')
        elif args.command == 'finalize':
            if outcome(state) != 'success' and not state.get('error_stage'):
                state['error_stage'] = 'switch_or_cancellation' if state.get('started') else 'production_preparation'
            if state['deployment_id']:
                api.status(state, outcome(state))
                state['terminal_status'] = outcome(state)
        elif args.command == 'report':
            with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as f:
                f.write(summary(state, api))
    except (ReleaseError, OSError, subprocess.SubprocessError, KeyError, ValueError) as error:
        # Никогда не сериализуем текст исключения: в нём могут быть URL запросов/секреты.
        if args.command == 'finalize':
            state['registration_error'] = 'finalize'
        if args.command not in ('report', 'finalize'):
            state['error_stage'] = state.get('error_stage') or args.command
        save(path, state)
        print(f'::error::Backend release {args.command}: ' + (str(error) if isinstance(error, ReleaseError) else 'operation failed; inspect stage logs'), file=sys.stderr)
        return 1
    save(path, state)
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (ReleaseError, OSError, KeyError, ValueError):
        print('::error::Release state or operation identity invalid', file=sys.stderr)
        sys.exit(1)
