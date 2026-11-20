#!/usr/bin/env python3
"""Verify the released macOS archives and record the resulting package provenance."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import tarfile


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('version')
    parser.add_argument('commit')
    parser.add_argument('inputs', type=Path)
    parser.add_argument('--record-package', type=Path)
    packaging = parser.add_mutually_exclusive_group()
    packaging.add_argument('--bootstrap-package', type=Path)
    packaging.add_argument('--runtime-only', action='store_true', help='Record a staged runtime package without a bootstrap')
    args = parser.parse_args()
    if not re.fullmatch(r'20\d{2}\.\d{1,2}\.\d{1,2}(?:\.[1-9]\d*)?(?:-beta\.\d+)?', args.version):
        parser.error('invalid tag')

    manifest = (args.inputs / 'release-manifest.json').read_bytes()
    metadata = json.loads(manifest)
    if metadata['schemaVersion'] != 1 or metadata['version'] != args.version or metadata['commit'] != args.commit:
        parser.error('release manifest does not match the tag checkout')

    if (args.bootstrap_package or args.runtime_only) and not args.record_package:
        parser.error('a package format requires --record-package')

    if args.record_package:
        package = args.record_package
        if package.name != f'Doomer-MDM-{args.version}.pkg':
            parser.error('unexpected package name')
        provenance = {
            'schemaVersion': 2 if args.runtime_only else 1,
            'releaseManifestSHA256': sha256(manifest),
            'packageSHA256': sha256(package.read_bytes()),
        }
        if args.bootstrap_package:
            if args.bootstrap_package.name != f'Doomer-MDM-Bootstrap-{args.version}.pkg':
                parser.error('unexpected bootstrap package name')
            provenance.update(schemaVersion=2, bootstrapSHA256=sha256(args.bootstrap_package.read_bytes()))
        (package.parent / 'mdm-release.json').write_text(json.dumps(provenance) + '\n')
        return

    for arch in ('arm64', 'amd64'):
        name = f'doomer_{args.version}_darwin_{arch}.tar.gz'
        archive = args.inputs / name
        if metadata['artifacts'][name] != 'sha256:' + sha256(archive.read_bytes()):
            parser.error(f'release archive checksum mismatch: {name}')
        with tarfile.open(archive, 'r:gz') as tar:
            members = [m for m in tar if m.name.removeprefix('./') == 'doomer']
            if len(members) != 1 or not members[0].isfile() or not 0 < members[0].size <= 64 * 1024 * 1024:
                parser.error(f'unexpected executable in {name}')
            with tar.extractfile(members[0]) as executable:
                (args.inputs / f'doomer-{arch}').write_bytes(executable.read())


if __name__ == '__main__':
    main()
