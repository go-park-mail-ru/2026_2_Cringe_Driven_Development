"""Release integrity and forward-only selection, without GitHub or VPS access."""
import hashlib
import json
import sys
from pathlib import Path
import tempfile
import unittest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import artifacts as a
from release import ReleaseError

SHA = 'a' * 40
REF = a.PACKAGE + '@sha256:' + 'd' * 64

class Integrity(unittest.TestCase):
    def files(self, root):
        (root / a.ARCHIVE).write_bytes(b'image')
        manifest = {'format_version': 1, 'source_sha': SHA, 'image_ref': REF,
                    'ci_url': a.RUN_ROOT + '10', 'build': {'platform': 'linux/amd64', 'image_id': 'sha256:' + 'e'*64},
                    'archive': {'name': a.ARCHIVE, 'size': 5, 'sha256': hashlib.sha256(b'image').hexdigest()}}
        (root / 'release.json').write_text(json.dumps(manifest))
        a.write_sums(root)
        return manifest

    def test_checksums_and_manifest_bind_archive_and_digest(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); manifest = self.files(root)
            self.assertEqual(a.verify_files(root, 'release-' + SHA), manifest)
            (root / a.ARCHIVE).write_bytes(b'other')
            with self.assertRaises(ReleaseError): a.verify_files(root, 'release-' + SHA)

    def test_rejects_wrong_sha_package_format_and_missing_checksum(self):
        for field, value in [('source_sha', 'b'*40), ('image_ref', REF.replace('/backend@', '/other@')), ('format_version', 2)]:
            with self.subTest(field=field), tempfile.TemporaryDirectory() as d:
                root = Path(d); manifest = self.files(root); manifest[field] = value
                (root / 'release.json').write_text(json.dumps(manifest)); a.write_sums(root)
                with self.assertRaises(ReleaseError): a.verify_files(root, 'release-' + SHA)
        with tempfile.TemporaryDirectory() as d:
            root = Path(d); self.files(root); (root / 'SHA256SUMS').write_text('')
            with self.assertRaises(ReleaseError): a.verify_files(root, 'release-' + SHA)

    def test_only_current_or_forward_release_is_allowed(self):
        self.assertIsNone(a.forward_guard(SHA, SHA, lambda *_: 'identical'))
        self.assertIsNone(a.forward_guard(SHA, 'b'*40, lambda *_: 'ahead'))
        for status in ['behind', 'diverged', 'unknown']:
            with self.assertRaises(ReleaseError): a.forward_guard(SHA, 'b'*40, lambda *_: status)

    def test_tag_is_exact(self):
        for tag in ['main', 'release-'+SHA+'\n', 'release-'+SHA[:7]]:
            with self.assertRaises(ReleaseError): a.tag_sha(tag)

from unittest.mock import patch
import os
import subprocess
from release import HistoricalRunMissing

class ReleaseAPI:
    prefix = '/repos/' + a.REPOSITORY
    def __init__(self):
        self.release = None
        self.assets = {}
        self.posts = []
        self.immutable = True
    def get(self, path):
        if path.startswith('/commits/'): return {'sha': SHA}
        if not self.release: raise HistoricalRunMissing('missing')
        return self.release
    def pages(self, path):
        return [self.release] if self.release else []
    def request(self, path, body, method=None):
        self.posts.append((path, body, method))
        if path.endswith('/generate-notes'): return {'body': '[PR #107](https://github.com/x/pull/107)'}, None
        if path.endswith('/releases'):
            self.release = dict(body, id=1, assets=[])
        else:
            self.release.update(body, immutable=self.immutable, published_at='2026-11-12T00:00:00Z', prerelease=False)
        return self.release, None
    def gh(self, *args):
        if args[:2] == ('release', 'upload'):
            path = Path(args[3]); self.assets[path.name] = path.read_bytes()
            self.release['assets'].append({'name': path.name})
        elif args[:2] == ('release', 'download'):
            name = args[4]; root = Path(args[6]); (root / name).write_bytes(self.assets[name])
        else: raise AssertionError(args)

class Publication(unittest.TestCase):
    def setUp(self):
        self.api = ReleaseAPI()
        self.directory = tempfile.TemporaryDirectory(); self.addCleanup(self.directory.cleanup)
        env = {'GITHUB_REPOSITORY': a.REPOSITORY, 'GITHUB_REF': 'refs/heads/main', 'GITHUB_EVENT_NAME': 'push',
               'GITHUB_WORKFLOW_REF': a.REPOSITORY + '/.github/workflows/ci.yml@refs/heads/main',
               'GITHUB_SHA': SHA, 'GITHUB_RUN_ID': '10', 'GITHUB_OUTPUT': self.directory.name + '/output'}
        self.addCleanup(patch.stopall)
        patch.dict(os.environ, env).start()
        patch.object(a, 'gh', side_effect=self.api.gh).start()
        patch.object(a, 'check_image').start()
        def archive(ref, root):
            (root / a.ARCHIVE).write_bytes(b'image')
            return 'sha256:' + 'e' * 64
        self.archive = patch.object(a, 'archive_image', side_effect=archive).start()
        self.tag = 'release-' + SHA

    def test_draft_assets_are_verified_before_immutable_publication(self):
        a.publish(self.api, self.tag, REF)
        self.assertFalse(self.api.release['draft'])
        self.assertEqual(set(self.api.assets), set(a.FILES))
        self.assertEqual(self.api.posts[-1][2], 'PATCH')
        self.assertIn('docker pull ' + REF, self.api.release['body'])
        self.assertEqual(a.resolve(self.api, self.tag)['image_ref'], REF)

    def test_published_rerun_reuses_without_build_or_mutation(self):
        a.publish(self.api, self.tag, REF); self.archive.reset_mock(); count = len(self.api.posts)
        a.preflight(self.api, self.tag); a.publish(self.api, self.tag, REF)
        self.archive.assert_not_called(); self.assertEqual(count, len(self.api.posts))
        self.assertIn('build=false', Path(os.environ['GITHUB_OUTPUT']).read_text())

    def test_interrupted_upload_resumes_saved_digest_without_rebuild(self):
        original = self.api.gh
        def failing(*args):
            if args[:2] == ('release', 'upload') and Path(args[3]).name == a.ARCHIVE:
                raise subprocess.CalledProcessError(1, 'gh')
            return original(*args)
        with patch.object(a, 'gh', side_effect=failing):
            with self.assertRaises(subprocess.SubprocessError): a.publish(self.api, self.tag, REF)
        self.assertTrue(self.api.release['draft']); self.assertEqual(set(self.api.assets), {'release.json'})
        a.preflight(self.api, self.tag)
        self.assertIn('build=false', Path(os.environ['GITHUB_OUTPUT']).read_text())
        a.publish(self.api, self.tag, REF)
        self.assertFalse(self.api.release['draft'])

    def test_missing_draft_mutable_tag_and_corruption_refuse_before_deployment(self):
        with self.assertRaises(ReleaseError): a.resolve(self.api, self.tag)
        a.publish(self.api, self.tag, REF)
        for change in [{'draft': True}, {'immutable': False}, {'tag_name': 'release-' + 'b'*40}]:
            original = dict(self.api.release); self.api.release.update(change)
            with self.assertRaises(ReleaseError): a.resolve(self.api, self.tag)
            self.api.release = original
        self.api.assets[a.ARCHIVE] = b'corrupted'
        with self.assertRaises(ReleaseError): a.resolve(self.api, self.tag)

    def test_anonymous_image_failure_and_upload_failure_do_not_publish(self):
        with patch.object(a, 'archive_image', side_effect=ReleaseError('image unavailable')):
            with self.assertRaises(ReleaseError): a.publish(self.api, self.tag, REF)
        self.assertIsNone(self.api.release)

    def test_publication_without_immutability_never_reports_success(self):
        self.api.immutable = False
        with self.assertRaises(ReleaseError): a.publish(self.api, self.tag, REF)

    def test_recovery_with_different_digest_or_archive_is_rejected(self):
        a.publish(self.api, self.tag, REF)
        with self.assertRaises(ReleaseError): a.publish(self.api, self.tag, REF[:-1]+'f')
        self.api.release['draft'] = True
        self.api.assets.pop(a.ARCHIVE)
        self.api.release['assets'] = [{'name': n} for n in self.api.assets]
        def different(ref, root):
            (root / a.ARCHIVE).write_bytes(b'new image')
            return 'sha256:' + 'e'*64
        with patch.object(a, 'archive_image', side_effect=different):
            with self.assertRaises(ReleaseError): a.publish(self.api, self.tag, REF)
        self.assertTrue(self.api.release['draft'])

    def test_untrusted_source_cannot_publish(self):
        for changed in [{'GITHUB_REF': 'refs/heads/topic'}, {'GITHUB_EVENT_NAME':'pull_request'},
                        {'GITHUB_WORKFLOW_REF': a.REPOSITORY+'/.github/workflows/other.yml@refs/heads/main'}]:
            with patch.dict(os.environ, changed):
                with self.assertRaises(ReleaseError): a.preflight(self.api, self.tag)

class DockerArchive(unittest.TestCase):
    def test_archive_roundtrip_uses_published_digest_and_image_identity(self):
        image_id='sha256:'+'f'*64
        calls=[]
        def docker(command,**kwargs):
            calls.append(command)
            if command[1]=='save': kwargs['stdout'].write(b'docker saved image')
            if command[1]=='load':
                import gzip
                self.assertEqual(gzip.decompress(kwargs['stdin'].read()),b'docker saved image')
            return subprocess.CompletedProcess(command,0)
        with tempfile.TemporaryDirectory() as d,patch.object(a,'check_image') as pull,patch.object(a.subprocess,'run',side_effect=docker),patch.object(a.subprocess,'check_output',return_value=image_id):
            self.assertEqual(a.archive_image(REF,Path(d)),image_id)
            pull.assert_called_once_with(REF)
            self.assertEqual(calls[0],['docker','tag',REF,a.PACKAGE+':release-archive'])
            self.assertEqual([c[1] for c in calls],['tag','save','image','load'])

    def test_loaded_different_image_is_rejected(self):
        with tempfile.TemporaryDirectory() as d,patch.object(a,'check_image'),patch.object(a.subprocess,'run'),patch.object(a.subprocess,'check_output',side_effect=['sha256:'+'a'*64,'sha256:'+'b'*64]):
            with self.assertRaisesRegex(ReleaseError,'docker load image differs'): a.archive_image(REF,Path(d))
