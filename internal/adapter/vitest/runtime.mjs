// An explicit modified execution profile. This runs installed Vitest, never a
// substitute test implementation. Native containment and source admission are
// separate producer obligations; protocol evidence grants no cache authority.
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const write = process.stdout.write.bind(process.stdout);
process.stdout.write = (...args) => process.stderr.write(...args);
let instance;
const fail = () => { throw new Error('restricted Vitest profile rejected'); };
const root = path.dirname(path.dirname(process.argv[1]));
const relative = filename => {
  const value = path.relative(root, filename).split(path.sep).join('/');
  if (!value || value.startsWith('../') || value === '..' || path.isAbsolute(value)) fail();
  return value;
};

async function closeChecked() {
  const current = instance;
  let failed = false;
  const original = current.logger.error;
  current.logger.error = function (...args) { failed = true; return original.apply(this, args); };
  try { await current.close(); }
  finally { current.logger.error = original; instance = undefined; }
  if (failed) fail();
}

try {
  const input = JSON.parse(process.argv[2]);
  if (input.schema !== 1 || input.profile !== 'vitest-4.1.11-vite-8.2.2-rolldown-1.2.12-trusted-managed-compiler-v1-threads-v1' || input.compiler !== 'trusted-managed-compiler-v1' || !['collect', 'run'].includes(input.mode) || process.versions.node !== '22.18.0' || process.env.CI !== 'true' || typeof WebAssembly !== 'object' || process.execArgv.some(arg => arg === '--jitless' || arg === '--no-expose-wasm')) fail();
  if (!Array.isArray(input.tests) || !input.tests.length || input.tests.length > 128 || new Set(input.tests).size !== input.tests.length) fail();
  for (const file of input.tests) if (typeof file !== 'string' || !/^[A-Za-z0-9_./-]+\.(?:js|ts)$/.test(file) || file.split('/').some(part => !part || part === '.' || part === '..')) fail();
  for (const [name, version] of [['vitest', '4.1.11'], ['vite', '8.2.2'], ['rolldown', '1.2.12']]) {
    if (JSON.parse(fs.readFileSync(path.join(root, 'node_modules', name, 'package.json'), 'utf8')).version !== version) fail();
  }
  const api = await import(pathToFileURL(path.join(root, 'node_modules/vitest/dist/node.js')).href);
  instance = await api.createVitest('test', {
    root, config: false, watch: false, cache: false, include: input.tests,
    environment: 'node', pool: 'threads', minWorkers: 1, maxWorkers: 1,
    isolate: true, fileParallelism: false, globals: false, testTimeout: 0,
    hookTimeout: 0, retry: 0, allowOnly: false, reporters: [],
    sequence: { shuffle: false }, passWithNoTests: false,
  }, { root, configFile: false, cacheDir: '/scratch/vite-cache', server: { watch: null } });
  const config = instance.config;
  if (config.watch || config.config || config.globals || config.fileParallelism || config.maxWorkers !== 1 || config.pool !== 'threads' || config.testTimeout !== 0 || config.hookTimeout !== 0 || config.retry !== 0 || config.coverage?.enabled || config.setupFiles?.length || config.globalSetup?.length || config.projects?.length || config.browser?.enabled || config.typecheck?.enabled || config.testNamePattern || config.changed || config.related?.length || config.shard || config.tagsFilter?.length || config.dangerouslyIgnoreUnhandledErrors || config.sequence.shuffle || !config.isolate) fail();
  if (instance.projects.length !== 1 || instance.projects[0].name !== '') fail();
  const specifications = await instance.globTestSpecifications();
  if (specifications.length !== input.tests.length) fail();
  const identities = new Map();
  for (const spec of specifications) {
    const file = relative(spec.moduleId), project = spec.project.name;
    if (!input.tests.includes(file) || project !== '' || spec.pool !== 'threads' || identities.has(file)) fail();
    identities.set(file, `${input.workspace}::${file}`);
  }
  let selected = specifications;
  if (input.selected !== null) {
    if (input.mode !== 'run' || !Array.isArray(input.selected) || !input.selected.length || new Set(input.selected).size !== input.selected.length || input.selected.some(id => ![...identities.values()].includes(id))) fail();
    selected = specifications.filter(spec => input.selected.includes(identities.get(relative(spec.moduleId))));
  }
  await instance.standalone();
  const result = input.mode === 'collect' ? await instance.collectTests(selected) : await instance.runTestSpecifications(selected, input.selected === null);
  const files = [], cases = [], tests = [], seen = new Set();
  for (const module of result.testModules) {
    const file = relative(module.moduleId), project = module.project.name;
    const unit = identities.get(file);
    if (!unit || project !== '' || seen.has(unit) || !selected.some(spec => identities.get(relative(spec.moduleId)) === unit)) fail();
    seen.add(unit);
    const state = module.state();
    if (input.mode === 'collect' && !['queued', 'pending', 'passed'].includes(state)) fail();
    if (input.mode === 'run' && !['passed', 'failed', 'skipped'].includes(state)) fail();
    files.push(input.mode === 'collect' ? { file, project } : { file, project, action: state });
    const counts = new Map();
    for (const test of module.children.allTests()) {
      if (!test.fullName || test.fullName.length > 256) fail();
      const occurrence = counts.get(test.fullName) || 0;
      counts.set(test.fullName, occurrence + 1);
      const identity = JSON.stringify([test.fullName, occurrence]);
      cases.push({ file, project, test: identity });
      if (cases.length > 1000) fail();
      if (input.mode === 'run') tests.push({ file, project, test: identity, action: test.result().state });
    }
  }
  if (seen.size !== selected.length || !cases.length) fail();
  const errors = result.unhandledErrors.length;
  if (input.mode === 'collect' && errors) fail();
  await closeChecked();
  if (input.mode === 'collect') write(JSON.stringify({ schema: 1, version: '4.1.11', viteVersion: '8.2.2', files, cases }) + '\n');
  else {
    write(JSON.stringify({ schema: 1, complete: true, errors, files, tests }) + '\n');
    if (errors || files.some(file => file.action === 'failed') || tests.some(test => test.action === 'failed')) process.exitCode = 1;
  }
} catch {
  process.stderr.write('Hayaku restricted Vitest native invocation failed\n');
  process.exitCode = 2;
} finally {
  if (instance) {
    try { await closeChecked(); } catch { process.exitCode = 2; }
  }
}
