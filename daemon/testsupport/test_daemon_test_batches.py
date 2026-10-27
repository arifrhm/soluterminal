import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('daemon-test-batches.py')
spec = importlib.util.spec_from_file_location('batches', SCRIPT)
batches = importlib.util.module_from_spec(spec)
spec.loader.exec_module(batches)


def event(action, name=None):
    value = {'Package': 'fixture', 'Action': action}
    if name:
        value['Test'] = name
    return json.dumps(value)


class CoverageTests(unittest.TestCase):
    def test_worker_count_respects_visible_cpus_and_existing_ceiling(self):
        for host, affinity, expected in [(2, {0, 1}, 2), (64, {3}, 1), (64, set(range(8)), 4), (1, {0}, 1), (None, {0, 1}, 1)]:
            with self.subTest(host=host, affinity=affinity), patch.object(os, 'cpu_count', return_value=host), patch.object(os, 'sched_getaffinity', return_value=affinity, create=True):
                self.assertEqual(batches.worker_count(), expected)
        for error in (OSError('unavailable'), AttributeError('unsupported')):
            with patch.object(os, 'cpu_count', return_value=2), patch.object(os, 'sched_getaffinity', side_effect=error, create=True):
                self.assertEqual(batches.worker_count(), 2)

    def test_partition_covers_every_name_exactly_once(self):
        names = [f'TestCase{i}' for i in range(1000)] + ['Example', 'FuzzParser']
        groups = batches.partition(names)
        self.assertEqual(sorted(sum(groups, [])), sorted(names))
        self.assertEqual(groups, batches.partition(names))
        for bad in ([], ['TestA', 'TestA']):
            with self.assertRaises(ValueError):
                batches.partition(bad)

    def test_success_requires_package_and_every_test(self):
        complete = [event('pass', 'TestA'), event('skip', 'TestB'), event('pass')]
        self.assertTrue(batches.audit_events(complete, 'fixture', ['TestA', 'TestB'])['passed'])
        for bad in (complete[:-1], complete[1:], complete + [event('pass', 'TestUnexpected')],
                    complete + [event('fail', 'TestA/subtest')], complete + [event('fail')]):
            self.assertFalse(batches.audit_events(bad, 'fixture', ['TestA', 'TestB'])['passed'])
        with self.assertRaises(ValueError):
            batches.audit_events(complete + [event('pass', 'TestA')], 'fixture', ['TestA', 'TestB'])
        with self.assertRaises(ValueError):
            batches.audit_events(['not json'], 'fixture', ['TestA'])

    def test_wall_deadline_is_shared_and_kills_child(self):
        with tempfile.TemporaryDirectory() as root:
            runner = batches.Runner(root, False)
            runner.deadline = time.monotonic() + 0.1
            with self.assertRaises(TimeoutError):
                runner.command([sys.executable, '-c', 'import time; time.sleep(20)'], Path(root) / 'log')
            self.assertFalse(runner.children)
            with self.assertRaises(TimeoutError):
                runner.command([sys.executable, '-c', 'pass'], Path(root) / 'next')

    def test_end_to_end_accounts_for_other_packages_and_failed_coverage(self):
        fake = '''#!/usr/bin/env python3
import json,os,re,sys
a=sys.argv[1:]
if a[0]=='list': print('go: downloading example.invalid/fixture v1.0.0',file=sys.stderr)
if a == ['list','-buildvcs=false','.']: print('fixture'); sys.exit(0)
if a == ['list','-buildvcs=false','./...']: print('fixture\\nfixture/child'); sys.exit(0)
if '-c' in a:
 from pathlib import Path
 binary=Path(a[a.index('-o')+1])
 binary.write_text('#!/usr/bin/env python3\\nprint("TestAlpha\\\\nTestBeta\\\\nExampleSample\\\\nFuzzParser")\\n')
 binary.chmod(0o700)
 sys.exit(1 if os.environ.get('SCENARIO')=='compile-fail' else 0)
if 'fixture/child' in a:
 print('ok fixture/child 0.1s')
 sys.exit(1 if os.environ.get('SCENARIO')=='child-fail' else 0)
assert os.environ['GOMAXPROCS']=='1' and os.environ['GOMEMLIMIT']=='625MiB'
assert a[:2]==['tool','test2json']
assert '-test.parallel=2' in a and '-test.count=1' in a
pattern=a[a.index('-test.run')+1]
for name in ['TestAlpha','TestBeta','ExampleSample','FuzzParser']:
 if re.fullmatch(pattern,name):
  if name=='TestBeta' and os.environ.get('SCENARIO')=='missing': continue
  print(json.dumps({'Package':'fixture','Action':'pass','Test':name}))
print(json.dumps({'Package':'fixture','Action':'pass'}))
sys.exit(1 if os.environ.get('SCENARIO')=='exit-fail' else 0)
'''
        cases = [(workers, scenario, succeeds) for workers in (1, 2, 4) for scenario, succeeds in [('ok', True), ('missing', False), ('child-fail', False), ('exit-fail', False), ('compile-fail', False)]]
        for workers, scenario, succeeds in cases:
            with self.subTest(workers=workers, scenario=scenario), tempfile.TemporaryDirectory() as root:
                root = Path(root)
                binary = root / 'go'
                binary.write_text(fake)
                binary.chmod(0o700)
                evidence = root / 'evidence'
                evidence.mkdir()
                with patch.object(batches, 'worker_count', return_value=workers), patch.dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'], SCENARIO=scenario), contextlib.redirect_stdout(io.StringIO()):
                    runner = batches.Runner(evidence, True)
                    try:
                        if succeeds:
                            runner.run()
                            inventory = json.loads((evidence / 'inventory.json').read_text())
                            self.assertEqual(inventory['packages'], ['fixture', 'fixture/child'])
                            self.assertEqual(inventory['workers'], workers)
                            self.assertIn('FuzzParser', inventory['tests'])
                        else:
                            with self.assertRaises(RuntimeError):
                                runner.run()
                    finally:
                        runner.stop()


if __name__ == '__main__':
    unittest.main()
