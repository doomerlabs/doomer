import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('publisher', Path(__file__).with_name('publish-r2.py'))
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)


class StorageError(Exception):
    def __init__(self, code):
        self.response = {'ResponseMetadata': {'HTTPStatusCode': code}}


class Store:
    def __init__(self):
        self.data = {}
        self.writes = []

    def put_object(self, Bucket, Key, Body, **options):
        existing = self.data.get(Key)
        if options.get('IfNoneMatch') == '*' and existing is not None:
            raise StorageError(412)
        if 'IfMatch' in options and (existing is None or publisher.digest(existing) != options['IfMatch']):
            raise StorageError(412)
        self.data[Key] = Body
        self.writes.append(Key)

    def get_object(self, Bucket, Key):
        if Key not in self.data:
            raise StorageError(404)
        data = self.data[Key]
        return {'Body': io.BytesIO(data), 'ETag': publisher.digest(data)}


class PublicationTests(unittest.TestCase):
    def test_managed_key_is_required_before_publication(self):
        with patch.dict(publisher.os.environ, {}, clear=True), patch.object(publisher.subprocess, 'run') as run:
            with self.assertRaises(ValueError):
                publisher.validate_managed_signing_key()
            run.assert_not_called()

    def test_managed_catalog_retries_with_concurrent_entries(self):
        class RacingStore(Store):
            raced = False
            def put_object(self, **kwargs):
                if kwargs['Key'] == 'cli/managed.json' and not self.raced:
                    self.raced = True
                    self.data['cli/managed.json'] = json.dumps({'versions': ['2026.11.6.1', '2026.11.6.3']}).encode()
                return super().put_object(**kwargs)
        store = RacingStore()
        store.data['cli/managed.json'] = json.dumps({'versions': ['2026.11.6.1']}).encode()
        def signer(args, check):
            previous = json.loads(Path(args[args.index('--previous') + 1]).read_text())
            versions = sorted(set(previous['versions'] + ['2026.11.6.2']))
            Path(args[args.index('--output') + 1]).write_text(json.dumps({'versions': versions}))
        with tempfile.TemporaryDirectory() as directory, patch.dict(publisher.os.environ, {'DOOMER_RELEASE_SIGNING_KEY': 'test'}), patch.object(publisher.subprocess, 'run', side_effect=signer) as run:
            publisher.publish_managed(store, 'bucket', Path(directory))
        self.assertEqual(run.call_count, 2)
        self.assertEqual(json.loads(store.data['cli/managed.json'])['versions'], ['2026.11.6.1', '2026.11.6.2', '2026.11.6.3'])

    def test_bucket_must_match_public_download_origin(self):
        self.assertEqual(publisher.validate_bucket('doomer-macos-releases'), 'doomer-macos-releases')
        with self.assertRaises(ValueError):
            publisher.validate_bucket('doomer-cli-releases')

    def fixture(self, path, version='2026.11.2'):
        files = {f'doomer_{version}_{s}_{a}.tar.gz': b'archive' for s in ('darwin', 'linux') for a in ('amd64', 'arm64')}
        files[f'doomer_{version}.spdx.json'] = b'{}'
        files['doomer-beta.rb' if '-beta.' in version else 'doomer.rb'] = b'formula'
        manifest = {'schemaVersion': 1, 'version': version, 'commit': 'a' * 40,
                    'artifacts': {n: 'sha256:' + publisher.digest(b) for n, b in files.items()}}
        files['release-manifest.json'] = json.dumps(manifest).encode()
        files['checksums.txt'] = ''.join(publisher.digest(b) + '  ' + n + '\n' for n, b in files.items()).encode()
        for name, data in files.items():
            (path / name).write_bytes(data)
        return files

    def test_publish_verifies_before_promoting_and_retries_are_idempotent(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            files = self.fixture(path)
            store = Store()
            store.data['stable.json'] = b'existing Mac manifest'
            checked = []
            def verify(url, data):
                self.assertNotIn('cli/stable.json', store.data)
                self.assertEqual(store.data[publisher.OBJECT_PREFIX + url.removeprefix(publisher.BASE_URL + '/')], data)
                checked.append(url)
            publisher.publish(store, 'bucket', path, '2026.11.2', verify)
            self.assertEqual(len(checked), len(files))
            self.assertEqual(store.writes[-1], 'cli/stable.json')
            self.assertEqual(store.data['stable.json'], b'existing Mac manifest')
            self.assertTrue(all(key.startswith('cli/') for key in store.writes))
            writes = len(store.writes)
            publisher.publish(store, 'bucket', path, '2026.11.2', lambda *args: None)
            self.assertEqual(len(store.writes), writes)

    def test_tamper_and_public_failure_never_promote(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            self.fixture(path)
            store = Store()
            store.data['stable.json'] = b'existing Mac manifest'
            store.data['cli/stable.json'] = b'existing CLI manifest'
            def fail(*args):
                raise RuntimeError('not publicly available')
            with self.assertRaises(RuntimeError):
                publisher.publish(store, 'bucket', path, '2026.11.2', fail)
            self.assertEqual(store.data['cli/stable.json'], b'existing CLI manifest')
            self.assertEqual(store.data['stable.json'], b'existing Mac manifest')
            self.assertTrue(all(key.startswith('cli/') for key in store.writes))
            key = 'cli/releases/2026.11.2/doomer.rb'
            store.data[key] = b'wrong'
            with self.assertRaises(ValueError):
                publisher.publish(store, 'bucket', path, '2026.11.2', lambda *args: None)
            self.assertEqual(store.data[key], b'wrong')
            self.assertEqual(store.data['stable.json'], b'existing Mac manifest')
            self.assertEqual(store.data['cli/stable.json'], b'existing CLI manifest')
            self.assertTrue(all(key.startswith('cli/') for key in store.writes))
            (path / 'doomer.rb').write_bytes(b'tampered')
            with self.assertRaises(ValueError):
                publisher.release_bundle(path, '2026.11.2')

    def test_beta_and_older_releases_do_not_change_stable(self):
        store = Store()
        publisher.promote(store, 'bucket', {'version': '2026.11.3'})
        stable = store.data['cli/stable.json']
        publisher.promote(store, 'bucket', {'version': '2026.11.2'})
        publisher.promote(store, 'bucket', {'version': '2026.11.4-beta.1'})
        self.assertEqual(store.data['cli/stable.json'], stable)
        self.assertIn('cli/beta.json', store.data)

    def test_daily_sequence_order_and_manifest(self):
        for earlier, later in [('2026.11.2', '2026.11.2.1'), ('2026.11.2.1', '2026.11.2.2'), ('2026.11.2.9', '2026.11.2.10'), ('2026.11.2.10', '2026.11.3.1')]:
            self.assertLess(publisher.version_key(earlier), publisher.version_key(later))
        for invalid in ['2026.11.2.0', '2026.11.2.01', '2026.11.2.1.2']:
            with self.assertRaises(ValueError):
                publisher.version_key(invalid)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            self.fixture(path, '2026.11.2.1')
            _, pointer = publisher.release_bundle(path, '2026.11.2.1')
            self.assertEqual(pointer['version'], '2026.11.2.1')
            self.assertIn('/2026.11.2.1/', pointer['assets']['darwin_arm64']['url'])

    def test_conditional_update_rechecks_concurrent_newer_release(self):
        class RacingStore(Store):
            def put_object(self, **kwargs):
                if kwargs['Key'] == 'cli/stable.json' and 'IfMatch' in kwargs:
                    self.data['cli/stable.json'] = json.dumps({'version': '2026.11.4'}).encode()
                return super().put_object(**kwargs)
        store = RacingStore()
        publisher.promote(store, 'bucket', {'version': '2026.11.1'})
        publisher.promote(store, 'bucket', {'version': '2026.11.2'})
        self.assertEqual(json.loads(store.data['cli/stable.json'])['version'], '2026.11.4')


if __name__ == '__main__':
    unittest.main()
