#!/usr/bin/env python3
"""Immutable release assets. gh handles authenticated asset redirects, never SSH."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
from release import GitHub, HistoricalRunMissing, PACKAGE, REPOSITORY, RUN_ROOT, ReleaseError, check_image, validate_image, validate_sha

ARCHIVE = 'backend-image.tar.gz'
FILES = (ARCHIVE, 'release.json', 'SHA256SUMS')


def tag_sha(tag):
    if not isinstance(tag, str) or not re.fullmatch(r'release-[0-9a-f]{40}', tag):
        raise ReleaseError('Expected release-<full lowercase 40-hex SHA>')
    return tag[8:]


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def write_sums(root):
    (root / 'SHA256SUMS').write_text(''.join(f'{digest(root / name)}  {name}\n' for name in FILES[:2]))


def verify_files(root, tag):
    sha = tag_sha(tag)
    expected = ''.join(f'{digest(root / name)}  {name}\n' for name in FILES[:2])
    if (root / 'SHA256SUMS').read_text() != expected:
        raise ReleaseError('Release SHA256SUMS mismatch')
    m = json.loads((root / 'release.json').read_text())
    if not isinstance(m, dict):
        raise ReleaseError('Release manifest must be an object')
    if type(m.get('format_version')) is not int or m['format_version'] != 1 or m.get('source_sha') != sha:
        raise ReleaseError('Release manifest format or source SHA mismatch')
    validate_image(m.get('image_ref'))
    if not re.fullmatch(re.escape(RUN_ROOT) + '[1-9][0-9]*', str(m.get('ci_url'))):
        raise ReleaseError('Invalid release CI URL')
    archive = m.get('archive', {})
    if not isinstance(archive, dict) or not isinstance(m.get('build'), dict):
        raise ReleaseError('Invalid release build/archive metadata')
    if (archive.get('name') != ARCHIVE or archive.get('size') != (root / ARCHIVE).stat().st_size
            or archive.get('sha256') != digest(root / ARCHIVE)
            or m.get('build', {}).get('platform') != 'linux/amd64'
            or not re.fullmatch(r'sha256:[0-9a-f]{64}', str(m.get('build', {}).get('image_id')))):
        raise ReleaseError('Release archive metadata mismatch')
    return m


def gh(*args):
    return subprocess.run(['gh', *args, '--repo', REPOSITORY], check=True, capture_output=True, text=True).stdout


def lookup(api, tag):
    try:
        return api.get('/releases/tags/' + tag)
    except HistoricalRunMissing:
        drafts = [r for r in api.pages('/releases?per_page=100') if r.get('tag_name') == tag]
        if len(drafts) > 1:
            raise ReleaseError('Ambiguous release tag')
        return drafts[0] if drafts else None


def download(tag, root, names=FILES):
    for name in names:
        gh('release', 'download', tag, '--pattern', name, '--dir', str(root))


def resolve(api, tag):
    sha = tag_sha(tag)
    release = lookup(api, tag)
    if not release or release.get('draft') is not False or release.get('prerelease') is not False or release.get('immutable') is not True:
        raise ReleaseError('Release missing, draft, prerelease or not immutable')
    if release.get('tag_name') != tag or not release.get('published_at'):
        raise ReleaseError('Release publication identity mismatch')
    # Resolve annotated and lightweight tags through the commit endpoint.
    if api.get('/commits/' + tag).get('sha') != sha:
        raise ReleaseError('Release tag does not resolve to its full source SHA')
    names = [x['name'] for x in release.get('assets', [])]
    if any(names.count(name) != 1 for name in FILES):
        raise ReleaseError('Release must have exactly one of each required asset')
    with tempfile.TemporaryDirectory(prefix='backend-release-') as directory:
        root = Path(directory)
        download(tag, root)
        manifest = verify_files(root, tag)
    return manifest


def forward_guard(base, target, compare):
    if base and base != target and compare(base, target) != 'ahead':
        raise ReleaseError('CD target is older or divergent; use Rollback explicitly')


def producer_guard():
    if (os.environ.get('GITHUB_REPOSITORY') != REPOSITORY or os.environ.get('GITHUB_REF') != 'refs/heads/main'
            or os.environ.get('GITHUB_EVENT_NAME') != 'push'
            or os.environ.get('GITHUB_WORKFLOW_REF') != REPOSITORY + '/.github/workflows/ci.yml@refs/heads/main'):
        raise ReleaseError('Release publication requires trusted CI push on main')


def output(**values):
    with open(os.environ['GITHUB_OUTPUT'], 'a') as stream:
        for key, value in values.items():
            stream.write(f'{key}={value}\n')


def preflight(api, tag):
    producer_guard()
    release = lookup(api, tag)
    if release and not release.get('draft'):
        manifest = resolve(api, tag); check_image(manifest['image_ref'])
        output(build='false', image_ref=manifest['image_ref'], release_tag=tag)
        return
    # A saved manifest is the checkpoint: resume its exact digest, never rebuild it.
    if release and any(x['name'] == 'release.json' for x in release.get('assets', [])):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); download(tag, root, ('release.json',))
            m = json.loads((root / 'release.json').read_text())
            if m.get('source_sha') != tag_sha(tag):
                raise ReleaseError('Draft checkpoint SHA mismatch')
            output(build='false', image_ref=validate_image(m.get('image_ref')), release_tag=tag)
    else:
        output(build='true', release_tag=tag)


def archive_image(ref, root):
    check_image(ref)
    image_id = subprocess.check_output(['docker', 'image', 'inspect', '--format', '{{.Id}}', ref], text=True).strip()
    # A stable local tag makes the archive directly usable after docker load.
    local = PACKAGE + ':release-archive'
    subprocess.run(['docker', 'tag', ref, local], check=True)
    with tempfile.TemporaryFile() as raw:
        subprocess.run(['docker', 'save', local], stdout=raw, check=True)
        raw.seek(0)
        with (root / ARCHIVE).open('wb') as target:
            with gzip.GzipFile(fileobj=target, mode='wb', mtime=0, filename='') as compressed:
                import shutil
                shutil.copyfileobj(raw, compressed)
    subprocess.run(['docker', 'image', 'rm', local], check=True)
    with (root / ARCHIVE).open('rb') as source:
        subprocess.run(['docker', 'load'], stdin=source, check=True)
    loaded = subprocess.check_output(['docker', 'image', 'inspect', '--format', '{{.Id}}', local], text=True).strip()
    if loaded != image_id:
        raise ReleaseError('docker load image differs from published build')
    return image_id


def publish(api, tag, ref):
    producer_guard(); sha = tag_sha(tag); validate_image(ref)
    if sha != validate_sha(os.environ['GITHUB_SHA']):
        raise ReleaseError('Publication target differs from CI source SHA')
    release = lookup(api, tag)
    if release and not release.get('draft'):
        m = resolve(api, tag)
        if m['image_ref'] != ref:
            raise ReleaseError('Published Release digest cannot be replaced')
        check_image(ref); return
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        existing = {x['name'] for x in release.get('assets', [])} if release else set()
        if set(FILES).issubset(existing):
            download(tag, root); m = verify_files(root, tag)
            if m['image_ref'] != ref:
                raise ReleaseError('Draft digest differs from checkpoint')
            check_image(ref)
        else:
            checkpoint = None
            if 'release.json' in existing:
                download(tag, root, ('release.json',))
                checkpoint = json.loads((root / 'release.json').read_text())
                if checkpoint.get('source_sha') != sha or checkpoint.get('image_ref') != ref:
                    raise ReleaseError('Draft checkpoint differs from build')
            image_id = archive_image(ref, root)
            m = {'format_version': 1, 'source_sha': sha, 'image_ref': ref,
                 'ci_url': RUN_ROOT + os.environ['GITHUB_RUN_ID'],
                 'build': {'platform': 'linux/amd64', 'image_id': image_id},
                 'archive': {'name': ARCHIVE, 'size': (root / ARCHIVE).stat().st_size, 'sha256': digest(root / ARCHIVE)}}
            if checkpoint:
                if checkpoint['build'] != m['build'] or checkpoint['archive'] != m['archive']:
                    raise ReleaseError('Recovered archive differs from draft checkpoint')
                m = checkpoint
            (root / 'release.json').write_text(json.dumps(m, indent=2) + '\n'); write_sums(root)
            verify_files(root, tag)
            if not release:
                notes = api.request(api.prefix + '/releases/generate-notes', {'tag_name': tag, 'target_commitish': sha})[0]
                body = notes['body'] + f'\n\nSource: `{sha}`\n\nCI: {m["ci_url"]}\n\n```sh\ndocker pull {ref}\n```\n'
                release = api.request(api.prefix + '/releases', {'tag_name': tag, 'target_commitish': sha,
                    'name': 'Backend ' + tag, 'body': body, 'draft': True})[0]
            # Manifest first: retries cannot silently change the build after this checkpoint.
            for name in ('release.json', ARCHIVE, 'SHA256SUMS'):
                if name not in existing:
                    gh('release', 'upload', tag, str(root / name))
            with tempfile.TemporaryDirectory() as verify:
                download(tag, Path(verify)); verify_files(Path(verify), tag)
        api.request(api.prefix + '/releases/' + str(release['id']), {'draft': False}, method='PATCH')
    resolve(api, tag)


def main():
    parser = argparse.ArgumentParser(); parser.add_argument('command', choices=['preflight', 'publish'])
    args = parser.parse_args()
    api = GitHub(os.environ.get('GH_TOKEN', ''))
    tag = 'release-' + validate_sha(os.environ['GITHUB_SHA'])
    if args.command == 'preflight': preflight(api, tag)
    else:
        publish(api, tag, os.environ['BACKEND_IMAGE_REF'])
        output(release_tag=tag)
        with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as stream:
            stream.write(f'## Release подготовлен\n\n[{tag}](https://github.com/{REPOSITORY}/releases/tag/{tag})\n\nВыкатка имеет отдельный CD run и Deployment.\n')


if __name__ == '__main__':
    try: main()
    except (ReleaseError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print('::error::' + (str(error) if isinstance(error, ReleaseError) else 'Release asset operation failed; see stage logs'))
        raise SystemExit(1)
