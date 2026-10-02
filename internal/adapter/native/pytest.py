import json
import pathlib
import sys
import pytest

request = json.loads(sys.argv[1])
sys.path.insert(0, request['cwd'])

class Results:
    def __init__(self):
        self.items = {}
        self.actions = {}
        self.errors = 0
    def pytest_collection_finish(self, session):
        for item in session.items:
            if item.nodeid in self.items:
                raise RuntimeError('duplicate native pytest identity')
            file = pathlib.Path(item.path).resolve(strict=True)
            selector = file.relative_to(pathlib.Path(request['cwd']).resolve()).as_posix()
            self.items[item.nodeid] = selector
        if len(self.items) > 100000:
            raise RuntimeError('native pytest inventory limit exceeded')
    def pytest_collectreport(self, report):
        if report.failed:
            self.errors += 1
    def pytest_runtest_logreport(self, report):
        if report.nodeid not in self.items:
            raise RuntimeError('unexpected native pytest result')
        prior = self.actions.get(report.nodeid)
        if report.failed:
            self.actions[report.nodeid] = 'fail'
        elif report.skipped and prior != 'fail':
            self.actions[report.nodeid] = 'skip'
        elif report.when == 'teardown':
            self.actions.setdefault(report.nodeid, 'pass')

results = Results()
code = int(pytest.main(request['args'], plugins=[results]))
complete = bool(results.items) and set(results.actions) == set(results.items) and code in (0, 1) and results.errors == 0
by_file = {}
tests = []
for identity, action in results.actions.items():
    selector = results.items[identity]
    tests.append({'selector': selector, 'test': identity, 'action': action})
    prior = by_file.get(selector)
    by_file[selector] = 'fail' if action == 'fail' or prior == 'fail' else ('pass' if action == 'pass' or prior == 'pass' else 'skip')
units = []
for selector, action in sorted(by_file.items()):
    expected = {identity for identity, file in results.items.items() if file == selector}
    if expected <= set(results.actions):
        units.append({'selector': selector, 'action': action})
payload = {'schema': 1, 'runner': 'pytest', 'version': pytest.__version__, 'complete': complete,
           'errors': results.errors, 'units': units, 'tests': tests}
serialized = json.dumps(payload, sort_keys=True)
if len(serialized.encode()) > 16 * 1024 * 1024:
    raise RuntimeError('native pytest metadata limit exceeded')
with open(request['output'], 'x', encoding='utf-8') as stream:
    stream.write(serialized)
sys.exit(code)
