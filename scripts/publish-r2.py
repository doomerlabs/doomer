#!/usr/bin/env python3
"""Publish verified CLI downloads or a signed MDM installer to R2."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.request

BASE_URL = 'https://downloads.doomer.ai/cli'
OBJECT_PREFIX = 'cli/'
RELEASE_BUCKET = 'doomer-macos-releases'
TAG = re.compile(r'^20\d{2}\.\d{1,2}\.\d{1,2}(?:\.[1-9][0-9]*)?(?:-beta\.\d+)?$')


def digest(data):
    return hashlib.sha256(data).hexdigest()


def release_bundle(directory, version):
    if not TAG.fullmatch(version):
        raise ValueError('Invalid release version')
    manifest = json.loads((directory / 'release-manifest.json').read_text())
    if manifest.get('schemaVersion') != 1 or manifest.get('version') != version:
        raise ValueError('Release manifest version mismatch')
    entries = {}
    for line in (directory / 'checksums.txt').read_text().splitlines():
        parts = line.split()
        if len(parts) != 2 or not re.fullmatch(r'[0-9a-f]{64}', parts[0]):
            raise ValueError('Invalid checksum entry')
        name = parts[1]
        if Path(name).name != name or name in entries:
            raise ValueError('Unsafe or duplicate checksum name')
        entries[name] = parts[0]
    expected = {f'doomer_{version}_{system}_{arch}.tar.gz' for system in ('darwin', 'linux') for arch in ('amd64', 'arm64')}
    expected |= {f'doomer_{version}.spdx.json', 'release-manifest.json', 'doomer-beta.rb' if '-beta.' in version else 'doomer.rb'}
    if set(entries) != expected or {p.name for p in directory.iterdir()} != expected | {'checksums.txt'}:
        raise ValueError('Unexpected release bundle membership')
    files = {}
    for name in sorted(expected | {'checksums.txt'}):
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise ValueError('Release files must be regular files')
        data = path.read_bytes()
        if name in entries and digest(data) != entries[name]:
            raise ValueError('Release checksum mismatch: ' + name)
        files[name] = data
    if manifest.get('artifacts') != {name: 'sha256:' + checksum for name, checksum in entries.items() if name != 'release-manifest.json'}:
        raise ValueError('Manifest/checksum mismatch')
    assets = {f'{system}_{arch}': {'url': f'{BASE_URL}/releases/{version}/doomer_{version}_{system}_{arch}.tar.gz',
              'sha256': entries[f'doomer_{version}_{system}_{arch}.tar.gz']}
              for system in ('darwin', 'linux') for arch in ('amd64', 'arm64')}
    pointer = {'schema_version': 1, 'version': version, 'commit': manifest['commit'], 'assets': assets}
    return files, pointer


def status(error):
    return getattr(error, 'response', {}).get('ResponseMetadata', {}).get('HTTPStatusCode')


def content_type(name):
    if name.endswith('.pkg'):
        return 'application/octet-stream'
    return 'application/gzip' if name.endswith('.gz') else 'application/json' if name.endswith('.json') else 'text/plain; charset=utf-8'


def put_immutable(client, bucket, key, data):
    try:
        client.put_object(Bucket=bucket, Key=key, Body=data, ContentType=content_type(key),
                          CacheControl='public, max-age=31536000, immutable', IfNoneMatch='*')
    except Exception as error:
        if status(error) != 412:
            raise
    # A retry may reuse exact bytes, but must never overwrite a published release.
    existing = client.get_object(Bucket=bucket, Key=key)['Body'].read()
    if existing != data:
        raise ValueError('Published object differs: ' + key)


def version_key(version):
    if not TAG.fullmatch(version):
        raise ValueError('Invalid channel version')
    main, _, beta = version.partition('-beta.')
    parts = tuple(map(int, main.split('.')))
    return parts + (0,) * (4 - len(parts)) + (int(beta) if beta else 0,)


def promote(client, bucket, pointer, managed_package=None):
    key = OBJECT_PREFIX + ('beta.json' if '-beta.' in pointer['version'] else 'stable.json')
    for _ in range(5):
        old = None
        previous = None
        try:
            current = client.get_object(Bucket=bucket, Key=key)
        except Exception as error:
            if status(error) != 404:
                raise
            condition = {'IfNoneMatch': '*'}
        else:
            previous = current['Body'].read()
            old = json.loads(previous)
            condition = {'IfMatch': current['ETag']}
            if old['version'] == pointer['version']:
                old_core = {k: v for k, v in old.items() if k not in ('releases', 'managed_package')}
                if old_core != pointer:
                    raise ValueError('Existing channel points to different release content')
        if managed_package is None:
            # Retain every verified CLI publication, including older releases.
            # This is discovery metadata, not an installation authority.
            effective = dict(old if old and version_key(old['version']) > version_key(pointer['version']) else pointer)
            history = {entry['version']: entry for entry in (old or {}).get('releases', [])}
            for entry in [old, pointer]:
                if entry and re.fullmatch(r'[a-f0-9]{40}', entry.get('commit', '')):
                    history[entry['version']] = {'version': entry['version'], 'commit': entry['commit']}
            if history:
                effective['releases'] = sorted(history.values(), key=lambda r: version_key(r['version']), reverse=True)
            if old and 'managed_package' in old:
                effective['managed_package'] = old['managed_package']
        else:
            if not old or version_key(old['version']) < version_key(pointer['version']):
                raise ValueError('Publish CLI downloads before promoting the managed package')
            effective = dict(old)
            current = old.get('managed_package')
            if not current or version_key(current['version']) < version_key(managed_package['version']):
                effective['managed_package'] = managed_package
            elif current['version'] == managed_package['version'] and current != managed_package:
                raise ValueError('Existing channel points to a different managed package manifest')
        data = (json.dumps(effective, sort_keys=True, indent=2) + '\n').encode()
        if data == previous:
            return
        try:
            client.put_object(Bucket=bucket, Key=key, Body=data, ContentType='application/json',
                              CacheControl='no-store', **condition)
            return
        except Exception as error:
            if status(error) != 412:
                raise
    raise RuntimeError('Channel changed repeatedly; retry publication')


def mdm_bundle(directory, version, manifest):
    package_name = f'Doomer-MDM-{version}.pkg'
    bootstrap_name = f'Doomer-MDM-Bootstrap-{version}.pkg'
    metadata_name = 'mdm-release.json'
    members = {p.name for p in directory.iterdir()}
    names = [package_name, metadata_name]
    if bootstrap_name in members:
        names.append(bootstrap_name)
    if members != set(names):
        raise ValueError('Unexpected MDM artifact membership')
    files = {}
    for name in names:
        path = directory / name
        limit = 4096 if name == metadata_name else 64 * 1024 * 1024
        if path.is_symlink() or not path.is_file() or path.stat().st_size > limit:
            raise ValueError('Invalid MDM artifact file: ' + name)
        files[name] = path.read_bytes()
        if name.endswith('.pkg') and not files[name].startswith(b'xar!'):
            raise ValueError('Invalid MDM package: ' + name)
    metadata = json.loads(files[metadata_name])
    expected = {
        'schemaVersion': 2 if metadata.get('schemaVersion') == 2 else 1,
        'releaseManifestSHA256': digest(manifest),
        'packageSHA256': digest(files[package_name]),
    }
    if bootstrap_name in files:
        expected.update(schemaVersion=2, bootstrapSHA256=digest(files[bootstrap_name]))
    if metadata != expected:
        raise ValueError('MDM package or source release checksum mismatch')
    return files


def publish_files(client, bucket, version, files, verify_public):
    for name, data in files.items():
        put_immutable(client, bucket, f'{OBJECT_PREFIX}releases/{version}/{name}', data)
    for name, data in files.items():
        verify_public(f'{BASE_URL}/releases/{version}/{name}', data)


def publish(client, bucket, directory, version, verify_public):
    files, pointer = release_bundle(directory, version)
    publish_files(client, bucket, version, files, verify_public)
    promote(client, bucket, pointer)
    print('Published ' + version + ' to ' + BASE_URL)


def publish_mdm(client, bucket, directory, mdm_directory, version, verify_public):
    release_files, pointer = release_bundle(directory, version)
    files = mdm_bundle(mdm_directory, version, release_files['release-manifest.json'])
    publish_files(client, bucket, version, files, verify_public)
    manifest = files['mdm-release.json']
    # Legacy packages install into live hook paths and cannot be auto-updated.
    if json.loads(manifest)['schemaVersion'] == 2:
        promote(client, bucket, pointer, {'version': version, 'manifest_sha256': digest(manifest)})
    print('Published MDM installer for ' + version + ' to ' + BASE_URL)


def validate_managed_signing_key():
    if not os.environ.get('DOOMER_RELEASE_SIGNING_KEY'):
        raise ValueError('DOOMER_RELEASE_SIGNING_KEY is required before publication')
    subprocess.run(['go', 'run', './scripts/sign-managed-catalog.go', '--check-key'], check=True)


def publish_managed(client, bucket, directory):
    """CAS publication preserves releases added by a concurrent publisher."""
    if not os.environ.get('DOOMER_RELEASE_SIGNING_KEY'):
        raise ValueError('DOOMER_RELEASE_SIGNING_KEY is required for managed releases')
    key = OBJECT_PREFIX + 'managed.json'
    with tempfile.TemporaryDirectory(prefix='doomer-channel-') as temp:
        previous = Path(temp) / 'previous.json'
        output = Path(temp) / 'managed.json'
        for _ in range(5):
            args = ['go', 'run', './scripts/sign-managed-catalog.go',
                    '--manifest', str(directory / 'release-manifest.json'), '--output', str(output)]
            try:
                current = client.get_object(Bucket=bucket, Key=key)
            except Exception as error:
                if status(error) != 404:
                    raise
                condition = {'IfNoneMatch': '*'}
            else:
                previous.write_bytes(current['Body'].read())
                args += ['--previous', str(previous)]
                condition = {'IfMatch': current['ETag']}
            subprocess.run(args, check=True)
            try:
                client.put_object(Bucket=bucket, Key=key, Body=output.read_bytes(),
                                  ContentType='application/json', CacheControl='no-store', **condition)
                return
            except Exception as error:
                if status(error) != 412:
                    raise
        raise RuntimeError('Managed channel changed repeatedly; retry publication')


def validate_bucket(bucket):
    if bucket != RELEASE_BUCKET:
        raise ValueError('R2_BUCKET must be ' + RELEASE_BUCKET)
    return bucket


def main():
    import boto3
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dir', type=Path, default=Path('dist'))
    parser.add_argument('--mdm-dir', type=Path, help='Publish the signed MDM artifacts and promote the managed package in its channel')
    parser.add_argument('--version', default=os.environ.get('GITHUB_REF_NAME'))
    args = parser.parse_args()
    bucket = validate_bucket(os.environ['R2_BUCKET'])
    account = os.environ['R2_ACCOUNT_ID']
    if not re.fullmatch(r'[0-9a-f]{32}', account):
        raise ValueError('Invalid R2 account ID')
    client = boto3.client('s3', endpoint_url=f'https://{account}.r2.cloudflarestorage.com', region_name='auto',
                          aws_access_key_id=os.environ['R2_ACCESS_KEY_ID'], aws_secret_access_key=os.environ['R2_SECRET_ACCESS_KEY'])
    def verify(url, data):
        request = urllib.request.Request(url, headers={'X-Doomer-Release-Verification': '1'})
        with urllib.request.urlopen(request, timeout=60) as response:
            if digest(response.read()) != digest(data):
                raise ValueError('Public artifact checksum mismatch')
    if args.mdm_dir is not None:
        publish_mdm(client, bucket, args.dir, args.mdm_dir, args.version, verify)
        return
    if '-beta.' not in args.version:
        validate_managed_signing_key()
    publish(client, bucket, args.dir, args.version, verify)
    if '-beta.' not in args.version:
        publish_managed(client, bucket, args.dir)


if __name__ == '__main__':
    main()
