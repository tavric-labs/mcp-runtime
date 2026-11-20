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
  f.write(json.dumps({'args':args,'cwd':os.getcwd(),'cache':os.environ.get('GOMODCACHE'),'proxy':os.environ.get('GOPROXY'),'sumdb':os.environ.get('GOSUMDB'),'private':os.environ.get('GOPRIVATE'),'toolchain':os.environ.get('GOTOOLCHAIN')})+'\\n')
 if os.environ.get('FAIL_COMMAND')==args[1] or os.environ.get('FAIL_COMMAND')=='checksum': sys.exit(75)
 if args[1]=='download':
  cache=pathlib.Path(os.environ['GOMODCACHE'])/'cache/download/example.org/fixture/@v'
  cache.mkdir(parents=True,exist_ok=True)
  (cache/'v1.0.0.zip').write_text('public fixture ZIP')
  (pathlib.Path(os.environ['GOMODCACHE'])/'excluded-extracted-file').write_text('do not package')
  if os.environ.get('READONLY_CACHE'):
   readonly=pathlib.Path(os.environ['GOMODCACHE'])/'readonly'
   readonly.mkdir(exist_ok=True)
   (readonly/'source.go').write_text('read-only cache fixture')
   readonly.chmod(0o555)
elif args==['clean','-modcache']:
 import shutil, stat
 cache=pathlib.Path(os.environ['GOMODCACHE'])
 if cache.exists():
  for root, dirs, files in os.walk(cache):
   pathlib.Path(root).chmod(0o700)
  shutil.rmtree(cache)
elif args[0]=='build':
 with open(os.environ['FIXTURE_LOG'],'a') as f:
  f.write(json.dumps({'args':args,'cwd':os.getcwd(),'cache':os.environ.get('GOMODCACHE'),'proxy':os.environ.get('GOPROXY'),'sumdb':os.environ.get('GOSUMDB'),'cgo':os.environ.get('CGO_ENABLED'),'goos':os.environ.get('GOOS'),'goarch':os.environ.get('GOARCH'),'toolchain':os.environ.get('GOTOOLCHAIN')})+'\\n')
 out=pathlib.Path(args[args.index('-o')+1])
 out.parent.mkdir(parents=True,exist_ok=True)
 out.write_text('compiled-server-binary')
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
        self.assertEqual([(Path(c['cwd']).name, c['args'][1]) for c in calls], [('repo', 'download'), ('fixture', 'download')])
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
        self.env['FAIL_COMMAND'] = 'checksum'
        self.assertNotEqual(self.run_script('pack').returncode, 0)
        self.assertFalse(self.archive.exists())

    def test_readonly_private_module_cache_is_cleaned(self):
        temporary = self.root / 'temporary'
        temporary.mkdir()
        self.env.update(TMPDIR=str(temporary), READONLY_CACHE='1')
        self.pack()
        self.assertEqual(list(temporary.iterdir()), [])
        cache = Path(json.loads(self.log.read_text().splitlines()[0])['cache'])
        self.assertFalse(cache.parent.exists())

    @unittest.skipUnless(os.environ.get('STAGING_GO_DEPS_REAL_GO') == '1', 'opt-in real Go regression; offline automation needs only Python and ShellCheck')
    def test_real_go_local_replacement_does_not_require_public_ziphash(self):
        # Resolve the installed pinned compiler before changing GOMODCACHE.
        goroot = subprocess.check_output(['go', 'env', 'GOROOT'], cwd=ROOT, text=True).strip()
        compiler = Path(goroot) / 'bin/go'
        (self.repo / 'go.mod').write_text('module mcp-runtime\n\ngo 1.27.4\n')
        (self.repo / 'services/fixture/go.mod').write_text('module fixture-service\n\ngo 1.27.4\n\nrequire mcp-runtime v0.0.0\nreplace mcp-runtime => ../..\n')
        metadata = self.bin / 'metadata-go'
        shutil.copyfile(self.bin / 'go', metadata)
        metadata.chmod(0o700)
        # Run real download/cleanup commands, with synthetic host metadata and
        # a tiny toolchain fixture so this regression stays offline and fast.
        cache_path = self.root / 'private-cache-path'
        (self.bin / 'go').write_text('''#!/usr/bin/env python3
import os, pathlib, sys
args=sys.argv[1:]
if args[0]=='clean':
 cache=pathlib.Path(os.environ['GOMODCACHE'])
 readonly=cache/'readonly'
 readonly.mkdir(parents=True,exist_ok=True)
 (readonly/'source.go').write_text('real Go cleanup fixture')
 readonly.chmod(0o555)
 pathlib.Path(''' + repr(str(cache_path)) + ''').write_text(str(cache))
program=''' + repr(str(compiler)) + ''' if args[0] in ('mod','clean') else ''' + repr(str(metadata)) + '''
os.execv(program,[program]+args)
''')
        self.pack()
        self.assertFalse(Path(cache_path.read_text()).parent.exists())
        self.assertFalse((self.repo / 'go.sum').exists())
        self.assertFalse((self.repo / 'services/fixture/go.sum').exists())

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

    def test_multitenancy_stages_offline_server_context_from_unpacked_go_inputs(self):
        self.pack()
        self.assertEqual(self.run_script('unpack').returncode, 0)
        example = self.repo / 'examples/oauth-example-go-2025-11-25'
        example.mkdir(parents=True)
        (example / 'go.mod').write_text('module oauth-example-go-2025-11-25\n\ngo 1.27.4\n')
        source = (ROOT / 'test/e2e/lib/staging.sh').read_text()
        self.assertIn('staging_prepare_multitenancy_server_context()', source)
        prepare_fn = 'staging_prepare_multitenancy_server_context() {' + source.split('staging_prepare_multitenancy_server_context() {', 1)[1].split('\n}\n', 1)[0] + '\n}\n'
        check_fn = 'staging_check_multitenancy() {' + source.split('staging_check_multitenancy() {', 1)[1].split('\n}\n', 1)[0] + '\n}\n'
        self.log.write_text('')
        context_dir = self.root / 'mt-server-image'
        env = dict(
            self.env,
            ROOT_DIR=str(self.repo),
            GOMODCACHE=str(self.destination / 'modules'),
            GOTOOLCHAIN='local',
        )
        result = subprocess.run(
            ['bash', '-c', prepare_fn + f'\nstaging_prepare_multitenancy_server_context "{context_dir}"'],
            env=env, text=True, capture_output=True, timeout=20,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = [json.loads(s) for s in self.log.read_text().splitlines()]
        self.assertEqual(len(calls), 1)
        self.assertEqual(Path(calls[0]['cwd']).name, 'oauth-example-go-2025-11-25')
        self.assertEqual(calls[0]['cache'], str(self.destination / 'modules'))
        self.assertEqual(calls[0]['proxy'], 'off')
        self.assertEqual(calls[0]['sumdb'], 'off')
        self.assertEqual(calls[0]['cgo'], '0')
        self.assertEqual(calls[0]['goos'], 'linux')
        self.assertEqual(calls[0]['goarch'], 'amd64')
        self.assertEqual(calls[0]['toolchain'], 'local')
        self.assertTrue((context_dir / 'oauth-example-go-2025-11-25').is_file())
        self.assertTrue((context_dir / 'ca-certificates.crt').is_file())
        dockerfile = (context_dir / 'Dockerfile').read_text()
        self.assertIn('FROM scratch', dockerfile)
        self.assertNotIn('gcr.io', dockerfile)
        self.assertNotIn('go mod download', dockerfile)
        self.assertIn('SERVER_CONTEXT="${server_context}"', check_fn)
        self.assertIn('SERVER_DOCKERFILE="${server_dockerfile}"', check_fn)


if __name__ == '__main__':
    unittest.main()
