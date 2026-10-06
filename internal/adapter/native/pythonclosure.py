"""Bounded descriptive Python AST/collection bridge; no isolation authority."""
import ast
import hashlib
import json
import os
import pathlib
import sys

request = json.loads(sys.argv[1])
root = pathlib.Path(request['root']).resolve(strict=True)
cwd = pathlib.Path(request['cwd']).resolve(strict=True)
mode = request['mode']
version = '.'.join(str(part) for part in sys.version_info[:3])
if not (3, 11) <= sys.version_info[:2] <= (3, 14):
    raise RuntimeError('unsupported interpreter')
payload = {'schema': 1, 'python_version': version}

def bounded_read(path, limit=16 * 1024 * 1024):
    with open(path, 'rb') as stream:
        data = stream.read(limit + 1)
    if len(data) > limit:
        raise RuntimeError('input limit')
    return data

if mode == 'identity':
    if request['runner'] == 'pytest':
        import pytest
        payload['runner_version'] = pytest.__version__
    else:
        import unittest
        payload['runner_version'] = version
    # Absolute runtime paths/ambient values are private hash inputs only.
    helper_directory = pathlib.Path(__file__).resolve().parent
    identity = [sys.version, sys.executable, sys.prefix, sys.base_prefix,
                ['<private-helper>' if pathlib.Path(entry).resolve() == helper_directory else entry for entry in sys.path],
                repr(sys.flags), sys.implementation.cache_tag]
    files = {str(pathlib.Path(sys.executable).resolve())}
    files.update(str(pathlib.Path(module.__file__).resolve())
                 for module in tuple(sys.modules.values())
                 if getattr(module, '__file__', None) and os.path.isfile(module.__file__))
    files.discard(str(pathlib.Path(__file__).resolve()))
    if len(files) > 10000:
        raise RuntimeError('runtime file count')
    total = 0
    for path in sorted(files):
        data = bounded_read(path, 64 * 1024 * 1024)
        total += len(data)
        if total > 128 * 1024 * 1024:
            raise RuntimeError('runtime size')
        identity.append([path, hashlib.sha256(data).hexdigest()])
    payload['runtime_digest'] = hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()

elif mode == 'collect':
    import pytest
    sys.path.insert(0, str(cwd))
    class Collector:
        def __init__(self):
            self.session = None
            self.errors = 0
        def pytest_collection_finish(self, session):
            self.session = session
        def pytest_collectreport(self, report):
            if report.failed:
                self.errors += 1
        def pytest_internalerror(self, excrepr, excinfo):
            self.errors += 1
        def pytest_keyboard_interrupt(self, excinfo):
            self.errors += 1
    collector = Collector()
    code = int(pytest.main(request['args'] + ['-p', 'no:cacheprovider', '--collect-only', '-q'], plugins=[collector]))
    if code != 0 or collector.errors or collector.session is None:
        raise RuntimeError('collection failed')
    items = [{'identity': item.nodeid, 'path': item.path.resolve().relative_to(root).as_posix()}
             for item in collector.session.items]
    if not items or len(items) > 100000:
        raise RuntimeError('collection count')
    payload.update(runner_version=pytest.__version__, items=items)

elif mode == 'analyze':
    gaps, effects, edges, sources, fixtures = set(), set(), set(), [], []
    def gap(code, owner=''):
        gaps.add((code, owner))
        if len(gaps) > 20000:
            raise RuntimeError('gap count')
    def effect(owner, kind, path=''):
        effects.add((owner, kind, path))
        if len(effects) > 100000:
            raise RuntimeError('effect count')
    roots = [cwd, root]
    for entry in os.environ.get('PYTHONPATH', '').split(os.pathsep):
        if entry:
            path = pathlib.Path(entry)
            path = (cwd / path).resolve() if not path.is_absolute() else path.resolve()
            if path.is_relative_to(root):
                roots.append(path)
            else:
                gap('external-import-path-unqualified')
    roots = list(dict.fromkeys(roots))
    modules, packages_by_owner = {}, {}
    files = request['files']
    if len(files) > 10000:
        raise RuntimeError('source count')
    for owner in files:
        path = root / owner
        for base in roots:
            if path.is_relative_to(base):
                relative = path.relative_to(base)
                parts = list(relative.with_suffix('').parts)
                if parts[-1] == '__init__':
                    parts.pop()
                if parts and all(part.isidentifier() for part in parts):
                    modules.setdefault('.'.join(parts), set()).add(owner)
                    name = '.'.join(parts)
                    package = name if path.name == '__init__.py' else name.rpartition('.')[0]
                    packages_by_owner.setdefault(owner, set()).add(package)
    def resolve(owner, name):
        matches = modules.get(name, set())
        # Bind package __init__ as well as the leaf; namespace directories are
        # membership-bound by the complete source snapshot, not imported here.
        for length in range(1, len(name.split('.'))):
            matches = matches | modules.get('.'.join(name.split('.')[:length]), set())
        if not matches:
            gap('installed-or-unresolved-import-unqualified', owner)
        if len(modules.get(name, set())) > 1:
            gap('ambiguous-import-unqualified', owner)
        for target in matches:
            edges.add((target, owner))
        if len(edges) > 100000:
            raise RuntimeError('import edge count')
    total, nodes = 0, 0
    for owner in files:
        owner.encode('utf-8', errors='strict')
        path = root / owner
        data = bounded_read(path)
        total += len(data)
        if total > 64 * 1024 * 1024:
            raise RuntimeError('source bytes')
        sources.append({'path': owner, 'digest': hashlib.sha256(data).hexdigest()})
        try:
            # ast.parse handles encoding cookies directly from bytes. Parse the
            # same bounded bytes whose digest was captured, without reopening.
            tree = ast.parse(data, filename=owner)
        except (SyntaxError, UnicodeError, LookupError):
            gap('source-syntax-or-encoding-unqualified', owner)
            effect(owner, 'unknown')
            continue
        aliases = {}
        packages = packages_by_owner.get(owner, set())
        for node in ast.walk(tree):
            nodes += 1
            if nodes > 1000000:
                raise RuntimeError('AST node count')
            if isinstance(node, ast.Import):
                for alias in node.names:
                    aliases[alias.asname or alias.name.split('.')[0]] = alias.name if alias.asname else alias.name.split('.')[0]
                    resolve(owner, alias.name)
            elif isinstance(node, ast.ImportFrom):
                names = {node.module or ''}
                if node.level:
                    names = set()
                    for package in packages:
                        parts = package.split('.') if package else []
                        if node.level > len(parts):
                            gap('relative-import-unresolved', owner)
                            continue
                        base = parts[:len(parts) - node.level + 1]
                        names.add('.'.join(base + ([node.module] if node.module else [])))
                for name in names:
                    if name:
                        resolve(owner, name)
                    for alias in node.names:
                        if alias.name == '*':
                            gap('wildcard-import-unqualified', owner)
                        else:
                            full = '.'.join(filter(None, [name, alias.name]))
                            aliases[alias.asname or alias.name] = full
                            if full in modules:
                                resolve(owner, full)
            elif isinstance(node, (ast.Global, ast.Nonlocal)):
                effect(owner, 'shared-state')
        def qualified(node):
            if isinstance(node, ast.Name):
                return aliases.get(node.id, node.id)
            if isinstance(node, ast.Attribute):
                return qualified(node.value) + '.' + node.attr
            return ''
        def literal(node):
            if isinstance(node, ast.Constant) and isinstance(node.value, str):
                return node.value
            if isinstance(node, ast.Call) and qualified(node.func) in ('pathlib.Path', 'Path') and len(node.args) == 1:
                return literal(node.args[0])
            if isinstance(node, ast.BinOp) and isinstance(node.op, (ast.Div, ast.Add)):
                left, right = literal(node.left), literal(node.right)
                if left is not None and right is not None:
                    return str(pathlib.Path(left) / right) if isinstance(node.op, ast.Div) else left + right
            return None
        def input_effect(kind, argument):
            value = literal(argument) if argument is not None else None
            if value is not None and '\x00' not in value:
                value.encode('utf-8', errors='strict')
                resolved = (cwd / value).resolve()
                if resolved.is_relative_to(root):
                    effect(owner, kind, resolved.relative_to(root).as_posix())
                    return
            effect(owner, kind)
            gap('dynamic-or-external-input-unqualified', owner)
        if pathlib.Path(owner).name == 'conftest.py':
            gap('fixture-lifecycle-unqualified', owner)
        for statement in tree.body:
            if isinstance(statement, (ast.Assign, ast.AnnAssign, ast.AugAssign)):
                effect(owner, 'shared-state')
            if not isinstance(statement, (ast.Import, ast.ImportFrom, ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
                gap('module-startup-unqualified', owner)
        for node in ast.walk(tree):
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                if node.name in ('load_tests', 'setUp', 'tearDown', 'setUpClass', 'tearDownClass', 'setUpModule', 'tearDownModule') or node.name.startswith(('pytest_', 'setup_', 'teardown_')):
                    gap('fixture-lifecycle-unqualified', owner)
                for decorator in node.decorator_list:
                    target = decorator.func if isinstance(decorator, ast.Call) else decorator
                    if qualified(target) == 'pytest.fixture':
                        scope, autouse = 'function', False
                        if isinstance(decorator, ast.Call):
                            for keyword in decorator.keywords:
                                if keyword.arg == 'scope' and literal(keyword.value) is not None:
                                    requested_scope = literal(keyword.value)
                                    if requested_scope in ('function', 'class', 'module', 'package', 'session'):
                                        scope = requested_scope
                                    else:
                                        scope = 'unknown'
                                        gap('dynamic-fixture-unqualified', owner)
                                elif keyword.arg == 'autouse' and isinstance(keyword.value, ast.Constant):
                                    autouse = bool(keyword.value.value)
                                else:
                                    gap('dynamic-fixture-unqualified', owner)
                        fixtures.append({'owner': owner, 'id': node.name, 'dependencies': [a.arg for a in node.args.posonlyargs + node.args.args + node.args.kwonlyargs], 'scope': scope, 'autouse': autouse})
                        gap('fixture-lifecycle-unqualified', owner)
            if isinstance(node, ast.Attribute):
                name = qualified(node)
                if name in ('os.environ', 'sys.path', 'sys.modules'):
                    effect(owner, 'environment' if name == 'os.environ' else 'shared-state')
                    if name.startswith('sys.'):
                        gap('dynamic-import-context-unqualified', owner)
            if not isinstance(node, ast.Call):
                continue
            name = qualified(node.func)
            tail = name.rsplit('.', 1)[-1]
            if name in ('importlib.import_module', '__import__'):
                module = literal(node.args[0]) if node.args else None
                if module is None or module.startswith('.'):
                    gap('dynamic-import-unqualified', owner)
                else:
                    resolve(owner, module)
            if name in ('open', 'io.open') or tail in ('read_text', 'read_bytes', 'write_text', 'write_bytes', 'exists', 'is_file', 'is_dir', 'stat', 'listdir', 'scandir', 'glob', 'rglob', 'iterdir') or name.startswith('os.path.'):
                kind = 'write' if tail.startswith('write_') else ('directory' if tail in ('listdir', 'scandir', 'glob', 'rglob', 'iterdir') else ('exists' if tail in ('exists', 'is_file', 'is_dir') else 'read'))
                argument = node.args[0] if node.args else None
                if isinstance(node.func, ast.Attribute) and tail in ('read_text', 'read_bytes', 'write_text', 'write_bytes', 'exists', 'is_file', 'is_dir', 'stat', 'iterdir', 'glob', 'rglob'):
                    argument = node.func.value
                if name in ('open', 'io.open'):
                    access = literal(node.args[1]) if len(node.args) > 1 else None
                    access = next((literal(k.value) for k in node.keywords if k.arg == 'mode'), access)
                    if access and any(char in access for char in 'wax+'):
                        kind = 'write'
                input_effect(kind, argument)
            for prefixes, kind in ((('os.getenv', 'os.environ.'), 'environment'), (('time.', 'datetime.'), 'clock'), (('random.', 'secrets.', 'uuid.'), 'random'), (('subprocess.', 'os.system', 'os.fork', 'os.exec', 'os.spawn'), 'subprocess'), (('socket.', 'urllib.', 'http.', 'requests.'), 'network'), (('ctypes.', 'cffi.'), 'native'), (('threading.', 'multiprocessing.', 'asyncio.', 'concurrent.'), 'concurrency')):
                if name.startswith(prefixes):
                    effect(owner, kind)
                    gap(kind + '-unqualified', owner)
            if tail in ('subTest', 'getfixturevalue'):
                gap('dynamic-case-or-fixture-unqualified', owner)
            # A descriptive syntax walk has no effect-free whitelist/authority.
            gap('call-semantics-unqualified', owner)
        if len(fixtures) > 50000:
            raise RuntimeError('fixture count')
    payload.update(sources=sources, edges=[{'from': a, 'to': b, 'reason': 'static-python-import'} for a, b in sorted(edges)], effects=[{'owner': a, 'kind': b, 'path': c} for a, b, c in sorted(effects)], fixtures=fixtures, gaps=[{'code': a, 'detail': b} for a, b in sorted(gaps)])
else:
    raise RuntimeError('unknown mode')

serialized = json.dumps(payload, sort_keys=True)
if len(serialized.encode()) > 16 * 1024 * 1024:
    raise RuntimeError('metadata bytes')
with open(request['output'], 'x', encoding='utf-8') as stream:
    stream.write(serialized)
