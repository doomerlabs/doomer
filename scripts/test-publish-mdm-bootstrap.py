#!/usr/bin/env python3
"""Test the GitHub bootstrap publication step with a local fake release."""

import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

VERSION = '2026.11.20.1'
PACKAGE = f'Doomer-MDM-Bootstrap-{VERSION}.pkg'
BYTES = b'signed bootstrap fixture'
FAKE_GH = '''#!/usr/bin/env python3
import os
from pathlib import Path
import shutil
import sys

root = Path(os.environ['TEST_RELEASE'])
draft = root / 'draft'
assets = root / 'assets'
args = sys.argv[2:]
action = args[0]
if action == 'view':
    if not draft.is_file():
        sys.exit(1)
    if 'isDraft' in args:
        print(draft.read_text())
    elif 'assets' in args:
        for file in assets.iterdir():
            print(file.name)
elif action == 'create':
    assert '--draft' in args and '--latest=false' in args
    draft.write_text('true')
elif action == 'upload':
    assert draft.read_text() == 'true' and '--clobber' not in args
    source = Path(args[2])
    assert not (assets / source.name).exists()
    shutil.copyfile(source, assets / source.name)
elif action == 'download':
    asset = args[args.index('--pattern') + 1]
    shutil.copyfile(assets / asset, Path(args[args.index('--dir') + 1]) / asset)
elif action == 'edit':
    assert '--draft=false' in args and '--latest=false' in args
    draft.write_text('false')
else:
    sys.exit(2)
'''


class BootstrapPublicationTest(unittest.TestCase):
    def test_resume_partial_draft_without_overwriting_existing_bytes(self):
        workflow = Path('.github/workflows/mdm-release.yml').read_text()
        step = workflow.split('- name: Publish the verified bootstrap package', 1)[1]
        script = textwrap.dedent(step.split('run: |\n', 1)[1])
        for existing_draft, expected in [('true', 0), ('false', 1)]:
            with self.subTest(draft=existing_draft), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                artifacts = root / 'mdm-artifact'
                artifacts.mkdir()
                (artifacts / PACKAGE).write_bytes(BYTES)
                assets = root / 'assets'
                assets.mkdir()
                (assets / PACKAGE).write_bytes(BYTES)
                (root / 'draft').write_text(existing_draft)
                gh = root / 'gh'
                gh.write_text(FAKE_GH)
                gh.chmod(0o755)
                env = os.environ | {
                    'PATH': f'{root}:{os.environ["PATH"]}',
                    'RUNNER_TEMP': str(root),
                    'TEST_RELEASE': str(root),
                    'TMPDIR': str(root),
                    'GITHUB_REF_NAME': f'mdm-bootstrap-{VERSION}',
                    'GITHUB_REPOSITORY': 'doomerlabs/doomer',
                }
                result = subprocess.run(['bash', '-c', script], capture_output=True, env=env)
                self.assertEqual(result.returncode, expected, result.stderr.decode())
                self.assertEqual((assets / PACKAGE).read_bytes(), BYTES)
                if expected == 0:
                    checksum = hashlib.sha256(BYTES).hexdigest()
                    self.assertEqual((assets / 'checksums.txt').read_text(), f'{checksum}  {PACKAGE}\n')
                    self.assertEqual((root / 'draft').read_text(), 'false')
                else:
                    self.assertFalse((assets / 'checksums.txt').exists())


if __name__ == '__main__':
    unittest.main()
