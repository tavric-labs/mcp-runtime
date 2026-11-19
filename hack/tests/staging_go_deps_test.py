"""Exercise staging dependency packaging and extraction without network or a VM."""
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'hack/ci/staging-go-deps.sh'


class StagingGoDepsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / 'repo'
        self.repo.mkdir()
        (self.repo / 'go.mod').write_text('module fixture\n\ngo 1.27.4\n')
        service = self.repo / 'services/fixture'
        service.mkdir(parents=True)
        (service / 'go.mod').write_text('module service\n\ngo 1.27.4\n')
        self.script = self.repo / 'hack/ci/staging-go-deps.sh'
        self.script.parent.mkdir(parents=True)
        if SCRIPT.exists():
            shutil.copyfile(SCRIPT, self.script)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.goroot = self.root / 'goroot'
        (self.goroot / 'bin').mkdir(parents=True)
        fake_go = '''#!/usr/bin/env python3
import json, os, pathlib, sys
args=sys.argv[1:]
if args[0]=='env':
 print({'GOVERSION':os.environ.get('FAKE_GOVERSION','go1.27.4'),'GOHOSTOS':'linux','GOHOSTARCH':'amd64','GOROOT':os.environ['FIXTURE_GOROOT']}[args[1]])
elif args[0]=='mod':
 with open(os.environ['FIXTURE_LOG'],'a') as f:
  f.write(json.dumps({'args':args,'cwd':os.getcwd(),'proxy':os.environ.get('GOPROXY'),'sumdb':os.environ.get('GOSUMDB'),'private':os.environ.get('GOPRIVATE'),'toolchain':os.environ.get('GOTOOLCHAIN')})+'\\n')
 if os.environ.get('FAIL_COMMAND')==args[1]: sys.exit(75)
 if args[1]=='download':
  cache=pathlib.Path(os.environ['GOMODCACHE'])/'cache/download/example.org/fixture/@v'
  cache.mkdir(parents=True,exist_ok=True)
  (cache/'v1.0.0.zip').write_text('public fixture ZIP')
  (pathlib.Path(os.environ['GOMODCACHE'])/'excluded-extracted-file').write_text('do not package')
else: sys.exit(80)
'''
        for go in [self.bin / 'go', self.goroot / 'bin/go']:
            go.write_text(fake_go)
            go.chmod(0o700)
        uname = self.bin / 'uname'
        uname.write_text('#!/bin/sh\ncase "$1" in -m) echo x86_64;; -s) echo Linux;; esac\n')
        uname.chmod(0o700)
        self.log = self.root / 'commands.jsonl'
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ['PATH'], FIXTURE_GOROOT=str(self.goroot), FIXTURE_LOG=str(self.log), GOPRIVATE='private.fixture', GONOSUMDB='*', GOPROXY='https://private.fixture', GOSUMDB='off', GOTOOLCHAIN='auto')
        self.archive = self.root / 'deps.tgz'
        self.destination = self.root / 'unpacked'

    def run_script(self, mode):
        args = ['bash', str(self.script), mode, str(self.archive)]
        if mode == 'unpack':
            args.append(str(self.destination))
        return subprocess.run(args, env=self.env, text=True, capture_output=True, timeout=20)

    def pack(self):
        result = self.run_script('pack')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_pack_verifies_each_module_and_excludes_unrelated_files(self):
        self.pack()
        calls = [json.loads(s) for s in self.log.read_text().splitlines()]
        self.assertEqual([(Path(c['cwd']).name, c['args'][1]) for c in calls], [('repo', 'download'), ('repo', 'verify'), ('fixture', 'download'), ('fixture', 'verify')])
        for call in calls:
            self.assertEqual(call['proxy'], 'https://proxy.golang.org')
            self.assertEqual(call['sumdb'], 'sum.golang.org')
            self.assertEqual(call['private'], '')
            self.assertEqual(call['toolchain'], 'local')
        with tarfile.open(self.archive) as archive:
            names = archive.getnames()
        self.assertIn('toolchain/bin/go', names)
        self.assertIn('modules/cache/download/example.org/fixture/@v/v1.0.0.zip', names)
        self.assertFalse(any('excluded-extracted' in name for name in names))

    def test_failed_download_does_not_publish_bundle(self):
        self.env['FAIL_COMMAND'] = 'download'
        self.assertNotEqual(self.run_script('pack').returncode, 0)
        self.assertFalse(self.archive.exists())

    def test_failed_verification_does_not_publish_bundle(self):
        self.env['FAIL_COMMAND'] = 'verify'
        self.assertNotEqual(self.run_script('pack').returncode, 0)
        self.assertFalse(self.archive.exists())

    def test_unpack_restores_matching_toolchain_and_download_cache(self):
        self.pack()
        result = self.run_script('unpack')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.destination / 'toolchain/bin/go').is_file())
        self.assertTrue((self.destination / 'modules/cache/download/example.org/fixture/@v/v1.0.0.zip').is_file())

    def test_corrupt_bundle_is_rejected_before_extraction(self):
        self.pack()
        with self.archive.open('ab') as archive:
            archive.write(b'corrupt')
        self.assertNotEqual(self.run_script('unpack').returncode, 0)
        self.assertFalse(self.destination.exists())

    def test_traversal_and_links_are_rejected_even_with_matching_digest(self):
        for name, link in [('../escaped', False), ('/escaped', False), ('toolchain/link', True)]:
            with self.subTest(name=name):
                with tarfile.open(self.archive, 'w:gz') as archive:
                    member = tarfile.TarInfo(name)
                    if link:
                        member.type = tarfile.SYMTYPE
                        member.linkname = '/tmp/escaped'
                    else:
                        member.size = 1
                    archive.addfile(member, None if link else io.BytesIO(b'x'))
                Path(str(self.archive) + '.sha256').write_text(hashlib.sha256(self.archive.read_bytes()).hexdigest() + '\n')
                self.assertNotEqual(self.run_script('unpack').returncode, 0)
                self.assertFalse(self.destination.exists())
        self.assertFalse((self.root / 'escaped').exists())

    def test_wrong_toolchain_is_rejected(self):
        self.pack()
        self.env['FAKE_GOVERSION'] = 'go1.26.10'
        self.assertNotEqual(self.run_script('unpack').returncode, 0)

    def test_vm_uses_bundle_and_persists_inputs_for_later_stages(self):
        self.pack()
        source = (ROOT / 'test/e2e/staging-vm.sh').read_text()
        function = 'stage_go_dependencies() {' + source.split('stage_go_dependencies() {', 1)[1].split('\n}\n', 1)[0] + '\n}\n'
        work = self.root / 'work'
        env = dict(self.env, ROOT_DIR=str(self.repo), WORK_DIR=str(work), E2E_GO_DEPS_ARCHIVE=str(self.archive))
        result = subprocess.run(['bash', '-c', function + '\nstaging_state_set() { printf "%s=%s\\n" "$1" "$2"; }\nstage_go_dependencies || exit\nprintf "path=%s\\n" "$PATH"'], env=env, text=True, capture_output=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('GOMODCACHE=' + str(work / 'go-inputs/modules'), result.stdout)
        self.assertIn('GOTOOLCHAIN=local', result.stdout)
        self.assertIn('path=' + str(work / 'go-inputs/toolchain/bin') + os.pathsep, result.stdout)

    def test_manual_vm_run_keeps_existing_toolchain_discovery(self):
        source = (ROOT / 'test/e2e/staging-vm.sh').read_text()
        function = 'stage_go_dependencies() {' + source.split('stage_go_dependencies() {', 1)[1].split('\n}\n', 1)[0] + '\n}\n'
        env = dict(self.env, E2E_GO_DEPS_ARCHIVE='')
        result = subprocess.run(['bash', '-c', function + '\nstage_go_dependencies'], env=env, text=True, capture_output=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
