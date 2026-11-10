#!/usr/bin/env python3
"""Provision only the private agent E2E bucket and dedicated test credentials.

Run through Doppler doomer/test. Never prints token values or signing material.
The provisioning token needs Workers R2 Storage Edit and token-management write access.
"""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import ssl
import shutil
import subprocess
import urllib.error
import urllib.request

BUCKET = 'doomer-agent-e2e'
API = 'https://api.cloudflare.com/client/v4'


def persist_issued_token(created, prefix, save, revoke, remove):
    """Roll back only this freshly issued token and its previously absent secrets."""
    names = [prefix + '_SECRET_ACCESS_KEY', prefix + '_ACCESS_KEY_ID']
    try:
        save(names[0], hashlib.sha256(created['value'].encode()).hexdigest())
        save(names[1], created['id'])
    except Exception as error:
        try:
            revoke(created['id'])
        except Exception:
            raise RuntimeError('Credential save failed; revoke newly created token ' + created['id'] + ' before retrying') from error
        try:
            remove(names)
        except Exception:
            raise RuntimeError('New token was revoked; remove incomplete secrets ' + ', '.join(names) + ' before retrying') from error
        raise RuntimeError('Credential save failed; new token and partial secrets were rolled back; retry provisioning') from error


def main():
    account = os.environ.get('E2E_CLOUDFLARE_ACCOUNT_ID', '')
    token = os.environ.get('E2E_CLOUDFLARE_API_TOKEN', '')
    if not re.fullmatch(r'[a-f0-9]{32}', account) or not token:
        raise SystemExit('Set E2E_CLOUDFLARE_ACCOUNT_ID and E2E_CLOUDFLARE_API_TOKEN in doomer/test')
    signing_names = ('E2E_CACHE_SIGNING_KEY', 'E2E_CACHE_SIGNING_KEY_ID', 'E2E_CACHE_PUBLIC_KEYS')
    if any(os.environ.get(name) for name in signing_names) and not all(os.environ.get(name) for name in signing_names):
        raise SystemExit('Incomplete existing E2E signing configuration; restore the matching key ID and public keyring before provisioning')
    if all(os.environ.get(name) for name in signing_names):
        try:
            private_key = base64.b64decode(os.environ['E2E_CACHE_SIGNING_KEY'], validate=True)
            keyring = json.loads(os.environ['E2E_CACHE_PUBLIC_KEYS'])
            key_id = os.environ['E2E_CACHE_SIGNING_KEY_ID']
            public_key = base64.b64decode(keyring[key_id], validate=True)
            if len(private_key) != 64 or len(public_key) != 32:
                raise ValueError('invalid key length')
            openssl = '/opt/homebrew/opt/openssl@3/bin/openssl' if Path('/opt/homebrew/opt/openssl@3/bin/openssl').exists() else shutil.which('openssl')
            seed_der = bytes.fromhex('302e020100300506032b657004220420') + private_key[:32]
            derived = subprocess.check_output([openssl, 'pkey', '-inform', 'DER', '-pubout', '-outform', 'DER'], input=seed_der, stderr=subprocess.DEVNULL)
            if derived[-32:] != public_key or private_key[32:] != public_key:
                raise ValueError('key mismatch')
        except (ValueError, KeyError, TypeError, subprocess.CalledProcessError):
            raise SystemExit('Invalid existing E2E signing configuration; restore the matching private key, key ID and public keyring') from None
    context = ssl.create_default_context()
    if Path('/etc/ssl/cert.pem').exists():
        context.load_verify_locations('/etc/ssl/cert.pem')

    def api(method, path, body=None):
        request = urllib.request.Request(API + path, method=method,
            data=None if body is None else json.dumps(body).encode(),
            headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(request, context=context, timeout=30) as response:
                data = json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f'Cloudflare {method} {path}: HTTP {error.code}') from None
        if not data.get('success'):
            raise RuntimeError(f'Cloudflare {method} {path} failed')
        return data['result']

    def save(name, value):
        subprocess.run(['doppler', 'secrets', 'set', name, '--project', 'doomer', '--config', 'test', '--silent'],
                       input=value.encode(), stdout=subprocess.DEVNULL, check=True)

    try:
        buckets = api('GET', f'/accounts/{account}/r2/buckets')['buckets']
        if not any(item['name'] == BUCKET for item in buckets):
            api('POST', f'/accounts/{account}/r2/buckets', {'name': BUCKET})
            print('Created private bucket:', BUCKET)
        can_manage_bucket = True
    except RuntimeError as error:
        if 'HTTP 403' not in str(error):
            raise
        token_base = f'/accounts/{account}/tokens' if token.startswith('cfat_') else '/user/tokens'
        identity = api('GET', token_base + '/verify')
        env = {key: value for key, value in os.environ.items() if not key.startswith('AWS_')}
        env.update(AWS_ACCESS_KEY_ID=identity['id'], AWS_SECRET_ACCESS_KEY=hashlib.sha256(token.encode()).hexdigest(),
                   AWS_EC2_METADATA_DISABLED='true', AWS_CONFIG_FILE='/dev/null', AWS_SHARED_CREDENTIALS_FILE='/dev/null')
        subprocess.run(['aws', '--endpoint-url', f'https://{account}.r2.cloudflarestorage.com',
                        '--region', 'auto', 's3api', 'head-bucket', '--bucket', BUCKET], env=env, check=True)
        can_manage_bucket = False
        print('Verified existing private bucket; lifecycle configuration requires R2 storage administration.')
    # No custom domain or public r2.dev access is enabled.
    if can_manage_bucket:
        existing = api('GET', f'/accounts/{account}/r2/buckets/{BUCKET}/lifecycle').get('rules', [])
        rules = [rule for rule in existing if rule['id'] != 'agent-e2e-retention']
        rules.append({'id': 'agent-e2e-retention', 'enabled': True,
                      'conditions': {'prefix': 'v1/'},
                      'deleteObjectsTransition': {'condition': {'type': 'Age', 'maxAge': 7 * 86400}}})
        api('PUT', f'/accounts/{account}/r2/buckets/{BUCKET}/lifecycle', {'rules': rules})
    token_base = f'/accounts/{account}/tokens' if token.startswith('cfat_') else '/user/tokens'
    permissions = api('GET', token_base + '/permission_groups')
    by_name = {item['name']: item['id'] for item in permissions}

    def credentials(prefix, name, permission):
        if os.environ.get(prefix + '_ACCESS_KEY_ID') and os.environ.get(prefix + '_SECRET_ACCESS_KEY'):
            print('Reusing', name)
            return
        if os.environ.get(prefix + '_ACCESS_KEY_ID') or os.environ.get(prefix + '_SECRET_ACCESS_KEY'):
            raise RuntimeError('Incomplete existing credentials: ' + prefix)
        created = api('POST', token_base, {
            'name': name, 'policies': [{'effect': 'allow',
                'permission_groups': [{'id': by_name[permission]}],
                'resources': {f'com.cloudflare.edge.r2.bucket.{account}_default_{BUCKET}': '*'}}]})
        # Persist the only copy of the token secret immediately, using stdin.
        def remove(names):
            subprocess.run(['doppler', 'secrets', 'delete', *names, '--project', 'doomer', '--config', 'test', '--yes', '--silent'], stdout=subprocess.DEVNULL, check=True)
        persist_issued_token(created, prefix, save, lambda token_id: api('DELETE', token_base + '/' + token_id), remove)
        print('Created bucket-scoped credentials:', name, '(token ID ' + created['id'] + ')')

    credentials('E2E_R2', 'doomer-agent-e2e-local', 'Workers R2 Storage Bucket Item Write')
    credentials('E2E_CI_R2', 'doomer-agent-e2e-ci-publisher', 'Workers R2 Storage Bucket Item Write')
    credentials('E2E_READ_R2', 'doomer-agent-e2e-reader', 'Workers R2 Storage Bucket Item Read')
    save('E2E_R2_ACCOUNT_ID', account)
    save('E2E_R2_BUCKET', BUCKET)
    if not os.environ.get('E2E_CACHE_SIGNING_KEY'):
        openssl = '/opt/homebrew/opt/openssl@3/bin/openssl' if Path('/opt/homebrew/opt/openssl@3/bin/openssl').exists() else shutil.which('openssl')
        private = subprocess.check_output([openssl, 'genpkey', '-algorithm', 'ED25519', '-outform', 'DER'])
        public = subprocess.check_output([openssl, 'pkey', '-inform', 'DER', '-pubout', '-outform', 'DER'], input=private)
        if len(private) != 48 or len(public) != 44:
            raise RuntimeError('Unexpected Ed25519 key encoding')
        key_id = 'agent-e2e-' + hashlib.sha256(public[-32:]).hexdigest()[:16]
        keyring = {key_id: base64.b64encode(public[-32:]).decode()}
        save('E2E_CACHE_SIGNING_KEY', base64.b64encode(private[-32:] + public[-32:]).decode())
        save('E2E_CACHE_SIGNING_KEY_ID', key_id)
        save('E2E_CACHE_PUBLIC_KEYS', json.dumps(keyring))
    else:
        keyring = json.loads(os.environ['E2E_CACHE_PUBLIC_KEYS'])
    destination = Path(__file__).resolve().parent.parent / 'tests/codexinstall/cache-keys.json'
    destination.write_text(json.dumps(keyring, indent=2) + '\n')
    print('Saved only public verification keys to', destination)
    print('Credential provisioning complete; the setup API token can be revoked after lifecycle configuration.')


if __name__ == '__main__':
    main()
