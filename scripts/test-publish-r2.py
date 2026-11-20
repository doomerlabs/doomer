import importlib.util
import io
import json
import os
import subprocess
import sys
import tarfile
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

    def test_signed_package_is_verified_and_immutable(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cli, mdm = root / 'cli', root / 'mdm'
            cli.mkdir()
            mdm.mkdir()
            version = '2026.11.2.1-beta.1'
            files = self.fixture(cli, version)
            name = f'Doomer-MDM-{version}.pkg'
            package = b'xar!signed-pkg-placeholder'
            (mdm / name).write_bytes(package)
            # Exercise the actual provenance output from the signing workflow.
            prepare = Path(__file__).with_name('prepare-mdm-release.py')
            subprocess.run([sys.executable, str(prepare), version, 'a' * 40, str(cli),
                            '--record-package', str(mdm / name)], check=True)

            store = Store()
            checked = []
            def verify(url, data):
                self.assertNotIn('cli/beta.json', store.data)
                checked.append(url)
            publisher.publish_mdm(store, publisher.RELEASE_BUCKET, cli, mdm, version, verify)
            key = f'cli/releases/{version}/{name}'
            self.assertEqual(store.data[key], package)
            self.assertIn(f'{publisher.BASE_URL}/releases/{version}/{name}', checked)
            self.assertEqual(store.writes, [key, f'cli/releases/{version}/mdm-release.json'])
            expected_writes = len(store.writes)
            publisher.publish_mdm(store, publisher.RELEASE_BUCKET, cli, mdm, version, lambda *args: None)
            self.assertEqual(len(store.writes), expected_writes)

            metadata = json.loads((mdm / 'mdm-release.json').read_bytes())
            metadata['releaseManifestSHA256'] = '0' * 64
            (mdm / 'mdm-release.json').write_text(json.dumps(metadata))
            invalid = Store()
            with self.assertRaisesRegex(ValueError, 'source release checksum mismatch'):
                publisher.publish_mdm(invalid, publisher.RELEASE_BUCKET, cli, mdm, version, lambda *args: None)
            self.assertEqual(invalid.writes, [])

            metadata['releaseManifestSHA256'] = publisher.digest(files['release-manifest.json'])
            (mdm / 'mdm-release.json').write_text(json.dumps(metadata))
            (mdm / name).write_bytes(b'xar!different')
            with self.assertRaisesRegex(ValueError, 'source release checksum mismatch'):
                publisher.publish_mdm(invalid, publisher.RELEASE_BUCKET, cli, mdm, version, lambda *args: None)
            self.assertEqual(invalid.writes, [])

            metadata['packageSHA256'] = publisher.digest(b'xar!different')
            (mdm / 'mdm-release.json').write_text(json.dumps(metadata))
            with self.assertRaisesRegex(ValueError, 'Published object differs'):
                publisher.publish_mdm(store, publisher.RELEASE_BUCKET, cli, mdm, version, lambda *args: None)
            self.assertEqual(store.data[key], package)

    def test_managed_pointer_advances_only_after_public_verification(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            store = Store()
            bucket = publisher.RELEASE_BUCKET
            feed = 'cli/beta.json'
            older = '2026.11.2.1-beta.1'
            newer = '2026.11.2.1-beta.2'

            def fixture(version):
                cli = root / version / 'cli'
                mdm = cli.parent / 'mdm'
                cli.mkdir(parents=True)
                mdm.mkdir()
                self.fixture(cli, version)
                package = mdm / f'Doomer-MDM-{version}.pkg'
                bootstrap = mdm / f'Doomer-MDM-Bootstrap-{version}.pkg'
                package.write_bytes(b'xar!signed-runtime')
                bootstrap.write_bytes(b'xar!signed-bootstrap')
                prepare = Path(__file__).with_name('prepare-mdm-release.py')
                subprocess.run([sys.executable, str(prepare), version, 'a' * 40, str(cli),
                                '--record-package', str(package), '--bootstrap-package', str(bootstrap)], check=True)
                return cli, mdm

            old_cli, old_mdm = fixture(older)
            new_cli, new_mdm = fixture(newer)
            publisher.publish(store, bucket, old_cli, older, lambda *_: None)

            def verify_before_promotion(url, data):
                self.assertNotIn('managed_package', json.loads(store.data[feed]))
                key = publisher.OBJECT_PREFIX + url.removeprefix(publisher.BASE_URL + '/')
                self.assertEqual(store.data[key], data)
                if url.endswith('/mdm-release.json'):
                    raise RuntimeError('checksum manifest is not public')

            with self.assertRaisesRegex(RuntimeError, 'manifest is not public'):
                publisher.publish_mdm(store, bucket, old_cli, old_mdm, older, verify_before_promotion)
            self.assertNotIn('managed_package', json.loads(store.data[feed]))
            publisher.publish_mdm(store, bucket, old_cli, old_mdm, older, lambda *_: None)
            old_pointer = json.loads(store.data[feed])['managed_package']
            self.assertEqual(old_pointer, {
                'version': older, 'manifest_sha256': publisher.digest((old_mdm / 'mdm-release.json').read_bytes()),
            })

            publisher.publish(store, bucket, new_cli, newer, lambda *_: None)
            self.assertEqual(json.loads(store.data[feed])['version'], newer)
            self.assertEqual(json.loads(store.data[feed])['managed_package'], old_pointer)
            publisher.publish_mdm(store, bucket, new_cli, new_mdm, newer, lambda *_: None)
            writes = list(store.writes)
            publisher.publish_mdm(store, bucket, old_cli, old_mdm, older, lambda *_: None)
            publisher.publish(store, bucket, new_cli, newer, lambda *_: None)
            self.assertEqual(store.writes, writes)
            self.assertEqual(json.loads(store.data[feed])['managed_package']['version'], newer)

    def test_mac_archives_must_match_manifest_before_packaging(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version = '2026.11.2.1'
            manifest = {'schemaVersion': 1, 'version': version, 'commit': 'b' * 40, 'artifacts': {}}
            for arch in ('arm64', 'amd64'):
                archive = root / f'doomer_{version}_darwin_{arch}.tar.gz'
                with tarfile.open(archive, 'w:gz') as tar:
                    member = tarfile.TarInfo('doomer')
                    member.size = len(arch)
                    tar.addfile(member, io.BytesIO(arch.encode()))
                manifest['artifacts'][archive.name] = 'sha256:' + publisher.digest(archive.read_bytes())
            (root / 'release-manifest.json').write_text(json.dumps(manifest))
            prepare = Path(__file__).with_name('prepare-mdm-release.py')
            args = [sys.executable, str(prepare), version, 'b' * 40, str(root)]
            subprocess.run(args, check=True)
            self.assertEqual((root / 'doomer-arm64').read_bytes(), b'arm64')
            self.assertEqual((root / 'doomer-amd64').read_bytes(), b'amd64')
            archive.write_bytes(b'tampered')
            failed = subprocess.run(args, capture_output=True)
            self.assertNotEqual(failed.returncode, 0)
            self.assertIn(b'checksum mismatch', failed.stderr)

    def test_signing_failure_allows_cli_publication_and_same_tag_package_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            calls = root / 'calls'
            clock = root / 'clock'
            clock.write_text('sleep() { SECONDS=$((SECONDS + $1)); }\n')
            gh = root / 'gh'
            gh.write_text(
                '#!/usr/bin/env bash\n'
                'printf "%s\\n" "$*" >> "$TEST_CALLS"\n'
                'if [[ "$1" == api ]]; then\n'
                '  if (( $(wc -l < "$TEST_CALLS") <= TEST_PENDING_POLLS )); then\n'
                '    printf "123\\tin_progress\\t\\n"\n'
                '  else\n'
                '    printf "123\\tcompleted\\t%s\\n" "$TEST_CONCLUSION"\n'
                '  fi\n'
                'fi\n'
            )
            gh.chmod(0o755)
            fetch = Path(__file__).with_name('fetch-mdm-release.sh')
            version = '2026.11.2.1-beta.1'
            cli = root / 'cli'
            cli.mkdir()
            self.fixture(cli, version)
            package_name = f'Doomer-MDM-{version}.pkg'
            package_key = f'cli/releases/{version}/{package_name}'
            store = Store()
            def verify(url, data):
                key = 'cli/' + url.removeprefix(publisher.BASE_URL + '/')
                self.assertEqual(store.data[key], data)
            env = os.environ | {'PATH': f'{root}:{os.environ["PATH"]}',
                                'GITHUB_REPOSITORY': 'doomerlabs/doomer',
                                'GITHUB_REF_NAME': version,
                                'BASH_ENV': str(clock), 'TEST_PENDING_POLLS': '0',
                                'TEST_CALLS': str(calls), 'TEST_CONCLUSION': 'failure'}
            mdm = root / 'artifact'
            args = ['bash', str(fetch), str(mdm)]
            failed = subprocess.run(args, env=env, capture_output=True)
            self.assertNotEqual(failed.returncode, 0)
            self.assertNotIn('run download', calls.read_text())
            publisher.publish(store, publisher.RELEASE_BUCKET, cli, version, verify)
            pointer = store.data['cli/beta.json']
            self.assertEqual(json.loads(pointer)['version'], version)
            self.assertNotIn(package_key, store.data)
            first_writes = list(store.writes)
            calls.unlink()
            env['TEST_CONCLUSION'] = 'success'
            env['TEST_PENDING_POLLS'] = str(85 * 60 // 15)
            subprocess.run(args, env=env, check=True, timeout=20)
            commands = calls.read_text().splitlines()
            self.assertEqual(len(commands), int(env['TEST_PENDING_POLLS']) + 2)
            commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
            self.assertIn(f'head_sha={commit}', commands[0])
            self.assertIn(f'Sign MDM installer {version}', commands[0])
            self.assertIn(f'run download 123 --repo doomerlabs/doomer --name mdm-{version}', commands[-1])
            mdm.mkdir()
            package = b'xar!signed-pkg-placeholder'
            (mdm / package_name).write_bytes(package)
            prepare = Path(__file__).with_name('prepare-mdm-release.py')
            subprocess.run([sys.executable, str(prepare), version, 'a' * 40, str(cli),
                            '--record-package', str(mdm / package_name)], check=True)
            publisher.publish_mdm(store, publisher.RELEASE_BUCKET, cli, mdm, version, verify)
            self.assertEqual(store.data[package_key], package)
            self.assertEqual(store.data['cli/beta.json'], pointer)
            self.assertEqual(store.writes, first_writes + [package_key, f'cli/releases/{version}/mdm-release.json'])

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

    def test_channel_history_retains_verified_releases_and_older_retries(self):
        store = Store()
        for version in ['2026.11.18.1-beta.1', '2026.11.18.1-beta.3', '2026.11.18.1-beta.2']:
            publisher.promote(store, 'bucket', {'version': version, 'commit': 'a' * 40})
        pointer = json.loads(store.data['cli/beta.json'])
        self.assertEqual(pointer['version'], '2026.11.18.1-beta.3')
        self.assertEqual([r['version'] for r in pointer['releases']], ['2026.11.18.1-beta.3', '2026.11.18.1-beta.2', '2026.11.18.1-beta.1'])
        before = len(store.writes)
        publisher.promote(store, 'bucket', {'version': '2026.11.18.1-beta.2', 'commit': 'a' * 40})
        self.assertEqual(len(store.writes), before)

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
