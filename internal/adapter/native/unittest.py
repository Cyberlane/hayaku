import json
import pathlib
import sys
import unittest

request = json.loads(sys.argv[1])
sys.path.insert(0, request['cwd'])
version = '.'.join(str(part) for part in sys.version_info[:3])
if sys.version_info[:2] < (3, 11) or sys.version_info[:2] > (3, 14):
    raise RuntimeError('unsupported interpreter version')
loader = unittest.TestLoader()
class CollectRunner:
    def __init__(self, **kwargs):
        pass
    def run(self, test):
        return None
# TestProgram is the native command parser, including discover and load_tests.
program = unittest.TestProgram(module=None, argv=['unittest'] + request['args'],
                               testLoader=loader, exit=False,
                               testRunner=CollectRunner)
if loader.errors:
    raise RuntimeError('native collection failed')

def flatten(suite):
    for test in suite:
        if isinstance(test, unittest.TestSuite):
            yield from flatten(test)
        else:
            yield test

cases = list(flatten(program.test))
if not cases or len(cases) > 100000:
    raise RuntimeError('invalid native case count')
seen = set()
items = {}
owners = {}
for test in cases:
    identity = test.id()
    if identity in seen:
        raise RuntimeError('duplicate native case identity')
    seen.add(identity)
    module = test.__class__.__module__
    path = getattr(sys.modules.get(module), '__file__', None)
    if not path:
        raise RuntimeError('native test module has no source identity')
    path = str(pathlib.Path(path).resolve(strict=True))
    entry = items.setdefault(module, {'selector': module, 'path': path, 'tests': []})
    if entry['path'] != path:
        raise RuntimeError('ambiguous native module source')
    entry['tests'].append(identity)
    owners[identity] = module

if request['mode'] == 'discover':
    payload = {'schema': 1, 'runner': 'unittest', 'version': version, 'complete': True,
               'items': sorted(items.values(), key=lambda entry: entry['selector'])}
    success = True
else:
    class Result(unittest.TestResult):
        def __init__(self):
            super().__init__()
            self.actions = {}
            self.subcounts = {}
            self.subresults = []
        def addSuccess(self, test):
            super().addSuccess(test)
            self.actions.setdefault(test.id(), 'pass')
        def addFailure(self, test, err):
            super().addFailure(test, err)
            self.actions[test.id()] = 'fail'
        def addError(self, test, err):
            super().addError(test, err)
            self.actions[test.id()] = 'fail'
        def addSkip(self, test, reason):
            super().addSkip(test, reason)
            self.actions[test.id()] = 'skip'
        def addExpectedFailure(self, test, err):
            super().addExpectedFailure(test, err)
            self.actions[test.id()] = 'skip'
        def addUnexpectedSuccess(self, test):
            super().addUnexpectedSuccess(test)
            self.actions[test.id()] = 'fail'
        def addSubTest(self, test, subtest, err):
            super().addSubTest(test, subtest, err)
            identity = test.id()
            ordinal = self.subcounts.get(identity, 0)
            self.subcounts[identity] = ordinal + 1
            # Parameters and exception contents are intentionally not retained.
            self.subresults.append({'selector': owners[identity],
                                    'test': json.dumps([identity, ordinal]),
                                    'action': 'fail' if err else 'pass'})
            if err:
                self.actions[identity] = 'fail'
        def stopTest(self, test):
            super().stopTest(test)
            self.actions.setdefault(test.id(), 'pass')
    result = Result()
    result.failfast = program.failfast
    result.buffer = program.buffer
    program.test.run(result)
    unexpected = set(result.actions) - seen
    if unexpected:
        raise RuntimeError('unexpected native case result')
    tests = [{'selector': owners[identity], 'test': identity, 'action': action}
             for identity, action in result.actions.items()] + result.subresults
    complete = set(result.actions) == seen and not result.shouldStop
    units = []
    for module, entry in sorted(items.items()):
        actions = [result.actions.get(identity) for identity in entry['tests']]
        if None in actions:
            continue
        action = 'fail' if 'fail' in actions else ('skip' if all(a == 'skip' for a in actions) else 'pass')
        units.append({'selector': module, 'action': action})
    # Class/module setup errors use synthetic ErrorHolder IDs not in inventory;
    # they are reported as errors and completeness remains false.
    errors = len(result.errors)
    success = result.wasSuccessful() and complete
    payload = {'schema': 1, 'runner': 'unittest', 'version': version, 'complete': complete,
               'errors': errors, 'units': units, 'tests': tests}

serialized = json.dumps(payload, sort_keys=True)
if len(serialized.encode()) > 16 * 1024 * 1024:
    raise RuntimeError('native metadata limit exceeded')
with open(request['output'], 'x', encoding='utf-8') as stream:
    stream.write(serialized)
sys.exit(0 if success else 1)
