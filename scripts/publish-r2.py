#!/usr/bin/env python3
"""Publish a verified release bundle, then atomically promote its channel pointer."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import urllib.request

BASE_URL = 'https://downloads.doomer.ai/cli'
OBJECT_PREFIX = 'cli/'
RELEASE_BUCKET = 'doomer-macos-releases'
TAG = re.compile(r'^20\d{2}\.\d{1,2}\.\d{1,2}(?:-beta\.\d+)?$')


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
    return tuple(map(int, main.split('.'))) + (int(beta) if beta else 0,)


def promote(client, bucket, pointer):
    key = OBJECT_PREFIX + ('beta.json' if '-beta.' in pointer['version'] else 'stable.json')
    data = (json.dumps(pointer, sort_keys=True, indent=2) + '\n').encode()
    for _ in range(5):
        try:
            current = client.get_object(Bucket=bucket, Key=key)
        except Exception as error:
            if status(error) != 404:
                raise
            condition = {'IfNoneMatch': '*'}
        else:
            previous = current['Body'].read()
            old = json.loads(previous)
            if version_key(old['version']) > version_key(pointer['version']):
                print('Newer channel version already published; leaving it unchanged.')
                return
            if old['version'] == pointer['version']:
                if previous != data:
                    raise ValueError('Existing channel points to different release content')
                return
            condition = {'IfMatch': current['ETag']}
        try:
            client.put_object(Bucket=bucket, Key=key, Body=data, ContentType='application/json',
                              CacheControl='no-store', **condition)
            return
        except Exception as error:
            if status(error) != 412:
                raise
    raise RuntimeError('Channel changed repeatedly; retry publication')


def publish(client, bucket, directory, version, verify_public):
    files, pointer = release_bundle(directory, version)
    for name, data in files.items():
        put_immutable(client, bucket, f'{OBJECT_PREFIX}releases/{version}/{name}', data)
    # Publish a discoverable channel only after every public artifact is readable.
    for name, data in files.items():
        verify_public(f'{BASE_URL}/releases/{version}/{name}', data)
    promote(client, bucket, pointer)
    print('Published ' + version + ' to ' + BASE_URL)


def validate_bucket(bucket):
    if bucket != RELEASE_BUCKET:
        raise ValueError('R2_BUCKET must be ' + RELEASE_BUCKET)
    return bucket


def main():
    import boto3
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dir', type=Path, default=Path('dist'))
    parser.add_argument('--version', default=os.environ.get('GITHUB_REF_NAME'))
    args = parser.parse_args()
    bucket = validate_bucket(os.environ['R2_BUCKET'])
    account = os.environ['R2_ACCOUNT_ID']
    if not re.fullmatch(r'[0-9a-f]{32}', account):
        raise ValueError('Invalid R2 account ID')
    client = boto3.client('s3', endpoint_url=f'https://{account}.r2.cloudflarestorage.com', region_name='auto',
                          aws_access_key_id=os.environ['R2_ACCESS_KEY_ID'], aws_secret_access_key=os.environ['R2_SECRET_ACCESS_KEY'])
    def verify(url, data):
        with urllib.request.urlopen(url, timeout=60) as response:
            if digest(response.read()) != digest(data):
                raise ValueError('Public artifact checksum mismatch')
    publish(client, bucket, args.dir, args.version, verify)


if __name__ == '__main__':
    main()
