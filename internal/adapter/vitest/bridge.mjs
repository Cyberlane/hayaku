// Private adapter protocol for pinned Vitest 3.2.7 and 4.1.11 APIs.
// Compatibility is not qualification of complete runtime influence.
// No package download, custom source parser, or related-test CLI is used.
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { builtinModules, createRequire } from 'node:module';
const input = JSON.parse(process.argv[2]);
const protocolWrite = process.stdout.write.bind(process.stdout);
// Configuration, transforms and workers may log; logs are never protocol data.
process.stdout.write = (...args) => process.stderr.write(...args);
let vitest;
let parseSource;
const readSource = filename => {
  const before = fs.lstatSync(filename, { bigint: true });
  if (!before.isFile() || before.isSymbolicLink() || before.size < 0n || before.size > 16n * 1024n * 1024n) throw new Error('unbounded native graph source');
  const fd = fs.openSync(filename, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW || 0));
  try {
    const opened = fs.fstatSync(fd, { bigint: true });
    if (!opened.isFile() || opened.dev !== before.dev || opened.ino !== before.ino || opened.size !== before.size || opened.mtimeNs !== before.mtimeNs || opened.ctimeNs !== before.ctimeNs) throw new Error('native graph source changed');
    // Read no more than the bounded original length plus one, even if a file
    // grows or is replaced after the path preflight. Never allocate its new size.
    const bytes = Buffer.alloc(Number(before.size) + 1);
    let used = 0;
    while (used < bytes.length) {
      const count = fs.readSync(fd, bytes, used, bytes.length - used, null);
      if (!count) break;
      used += count;
    }
    const after = fs.fstatSync(fd, { bigint: true });
    const pathAfter = fs.lstatSync(filename, { bigint: true });
    if (used === bytes.length || BigInt(used) !== before.size || !pathAfter.isFile() || pathAfter.isSymbolicLink() || after.dev !== before.dev || after.ino !== before.ino || pathAfter.dev !== before.dev || pathAfter.ino !== before.ino || after.size !== before.size || pathAfter.size !== before.size || after.mtimeNs !== before.mtimeNs || after.ctimeNs !== before.ctimeNs || pathAfter.mtimeNs !== before.mtimeNs || pathAfter.ctimeNs !== before.ctimeNs) throw new Error('native graph source changed');
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(0, used));
  } finally { fs.closeSync(fd); }
};
// This is bounded syntax evidence for unresolved imports, not a runtime proof.
// Use only a parser already supplied by the bound installed Vite toolchain.
const unresolvedImports = (source, filename, original = false, imports = new Set()) => {
  if (!parseSource || typeof source !== 'string' || Buffer.byteLength(source) > 16 * 1024 * 1024) return true;
  try {
    const options = original && /\.(?:[cm]?ts|tsx)$/.test(filename) ? { lang: filename.endsWith('tsx') ? 'tsx' : 'ts' } : {};
    const stack = [{ node: parseSource(source, options, filename), parent: null }];
    let remaining = 200000;
    const literal = node => node && ((['Literal', 'StringLiteral'].includes(node.type) && typeof node.value === 'string') || (node.type === 'TemplateLiteral' && !node.expressions.length));
    const add = node => {
      if (!literal(node)) return false;
      const value = node.type === 'TemplateLiteral' ? node.quasis[0]?.value?.cooked : node.value;
      if (typeof value !== 'string' || !value || Buffer.byteLength(value) > 4096 || value.includes('\0')) return false;
      imports.add(value);
      return true;
    };
    while (stack.length) {
      if (--remaining < 0) return true;
      const { node, parent } = stack.pop();
      if (!node || typeof node !== 'object') continue;
      if (['ImportExpression', 'ImportDeclaration', 'ExportAllDeclaration'].includes(node.type) && !add(node.source)) return true;
      if (node.type === 'ExportNamedDeclaration' && node.source && !add(node.source)) return true;
      if (node.type === 'Identifier' && ['eval', 'Function', 'require', 'createRequire'].includes(node.name)) return true;
      // A configured plugin may return SSR code with an escaped loader. Only a
      // direct, literal import call has a configured graph dependency contract.
      if (node.type === 'Identifier' && ['__vite_ssr_dynamic_import__', '__vite_ssr_import__'].includes(node.name) && !(parent?.type === 'CallExpression' && parent.callee === node && literal(parent.arguments[0]))) return true;
      if (node.type === 'CallExpression' && node.callee?.type === 'Identifier' && ['__vite_ssr_dynamic_import__', '__vite_ssr_import__'].includes(node.callee.name) && !add(node.arguments[0])) return true;
      for (const [key, value] of Object.entries(node)) {
        if (['loc', 'range', 'comments', 'tokens', 'parent'].includes(key)) continue;
        if (Array.isArray(value)) { for (const child of value) stack.push({ node: child, parent: node }); }
        else if (value && typeof value === 'object') stack.push({ node: value, parent: node });
      }
    }
    return false;
  } catch { return true; }
};
// These pinned APIs settle close failures and report them through the logger.
// Observe that API signal directly; user stderr is never an error oracle.
const closeChecked = async () => {
  const instance = vitest;
  let failed = false;
  const report = instance.logger.error;
  instance.logger.error = function (...args) {
    failed = true;
    return report.apply(this, args);
  };
  try { await instance.close(); }
  finally { instance.logger.error = report; vitest = undefined; }
  if (failed) throw new Error('Vitest close failed');
};
// Vite 7's ESM config bundler leaves this generated directory after removing
// its temporary module. Restore only an originally absent, still-empty 0755
// directory in the declared private copy; never change an external runtime.
const bundlerDirectory = path.join(input.cwd, 'node_modules', '.vite-temp');
const restoreBundlerDirectory = path.resolve(input.modules) === path.resolve(input.cwd, 'node_modules') && !fs.existsSync(bundlerDirectory);
const cleanBundlerDirectory = async () => {
  if (!restoreBundlerDirectory) return;
  for (let attempt = 0; attempt < 11; attempt++) {
    let info;
    try { info = await fs.promises.lstat(bundlerDirectory); } catch (error) { if (error.code === 'ENOENT') return; throw error; }
    if (!info.isDirectory() || info.isSymbolicLink() || (info.mode & 0o7777) !== 0o755) throw new Error('unexpected native config cache');
    try { await fs.promises.rmdir(bundlerDirectory); return; } catch (error) {
      if (error.code === 'ENOENT') return;
      if (error.code !== 'ENOTEMPTY' || attempt === 10) throw error;
      await new Promise(resolve => setTimeout(resolve, 10));
    }
  }
};
const relative = file => {
  const rel = path.relative(input.root, file).split(path.sep).join('/');
  if (!rel || rel === '..' || rel.startsWith('../') || path.isAbsolute(rel)) throw new Error('outside snapshot');
  return rel;
};
const dependencyFile = file => typeof file === 'string' && file.replaceAll('\\', '/').includes('/node_modules/');
const identity = spec => `${input.workspace}:${spec.project.name}:${relative(spec.moduleId)}`;
try {
  if (input.mode === 'observe') await import(pathToFileURL(process.env.HAYAKU_TRACE_HOOK).href);
  const pkg = JSON.parse(fs.readFileSync(path.join(input.modules, 'vitest/package.json'), 'utf8'));
  const require = createRequire(path.join(input.modules, 'vitest/package.json'));
  const vite = JSON.parse(fs.readFileSync(require.resolve('vite/package.json'), 'utf8'));
  const supported = (pkg.version === '3.2.7' && vite.version === '6.4.3') || (pkg.version === '4.1.11' && (vite.version === '7.3.1' || vite.version === '8.1.5' || vite.version === '8.2.2'));
  if (!supported) throw new Error('unsupported Vitest/Vite API versions');
  // Independent collection has its own actual installed-pair acceptance. Older
  // discovery/run compatibility does not silently qualify this newer API path.
  if (input.mode === 'collect' && !(pkg.version === '4.1.11' && vite.version === '8.2.2')) throw new Error('unsupported independent collection pair');
  if (input.mode === 'discover') {
    const viteRequire = createRequire(require.resolve('vite/package.json'));
    for (const parser of ['rolldown/parseAst', 'rollup/parseAst']) {
      try {
        const api = await import(pathToFileURL(viteRequire.resolve(parser)).href);
        if (typeof api.parseAst === 'function') { parseSource = api.parseAst; break; }
      } catch { /* Missing parser/extension is unknown evidence, never a fetch. */ }
    }
  }
  const legacy = pkg.version === '3.2.7';
  const api = await import(pathToFileURL(path.join(input.modules, 'vitest/dist/node.js')).href);
  const parsed = api.parseCLI(['vitest', ...input.args]);
  vitest = await api.createVitest('test', { ...parsed.options, watch: false, cache: false }, { cacheDir: path.join(path.dirname(process.argv[1]), 'vite-cache') });
  // Reject native modes that alter scope, write acceptance artifacts, or cannot
  // be reconciled by this qualified Node file protocol.
  const config = vitest.config;
  if (config.changed || config.related?.length || config.shard || config.standalone || config.bail || config.testNamePattern || config.tagsFilter?.length || config.coverage?.enabled || config.dangerouslyIgnoreUnhandledErrors || config.update === true || config.update === 'all' || config.update === 'new') throw new Error('unsupported configured Vitest execution mode');
  for (const project of vitest.projects) {
    if (project.config.browser?.enabled || project.config.typecheck?.enabled) throw new Error('unsupported configured Vitest project mode');
    if (!['forks', 'threads'].includes(project.config.pool) || project.config.poolMatchGlobs?.length) throw new Error('unsupported configured Vitest execution pool');
  }
  if (input.mode === 'observe') {
    // Diagnostics run serially; this is not the original full-run execution mode.
    // Prepend the hook ahead of user setup in each already configured project.
    const traceHook = fs.realpathSync(process.env.HAYAKU_TRACE_HOOK);
    for (const project of vitest.projects) {
      project.config.setupFiles = [traceHook, ...project.config.setupFiles || []];
      project.config.fileParallelism = false;
      if (legacy) {
        // vite-node transforms setup files. Permit only this exact private
        // observer file through Vite's loader, retaining every user allowance.
        // This diagnostic loader allowance is not a runtime sandbox.
        project.vite.config.server.fs.allow = [...project.vite.config.server.fs.allow, traceHook];
      }
    }
    vitest.config.fileParallelism = false;
    vitest.config.maxWorkers = 1;
  }
  const specs = await vitest.globTestSpecifications(parsed.filter);
  if (!specs.length || specs.length > 50000) throw new Error('empty or oversized inventory');
  const seen = new Set();
  for (const spec of specs) {
    if (!['forks', 'threads'].includes(spec.pool)) throw new Error('unsupported specification execution pool');
    const id = identity(spec);
    if (seen.has(id)) throw new Error('duplicate specification');
    seen.add(id);
  }
  if (input.mode === 'discover') {
    const scopes = [];
    const global = new Set();
    // The same configured transform closure applies to test and startup entries.
    // Setup/config helpers influence every file even when only one test imports
    // the helper directly. Missing/virtual transforms broaden the whole scope.
    const closure = async (project, entries) => {
      const files = new Set(), visited = new Set();
      let incomplete = false;
      const environment = legacy ? project.vite : project.vite.environments.ssr;
      const visit = async id => {
        if (visited.has(id)) return;
        visited.add(id);
        if (visited.size > 50000) throw new Error('graph limit');
        const result = legacy ? await project.vitenode.transformRequest(id) : await environment.transformRequest(id);
        if (!result) { incomplete = true; return; }
        const imports = new Set();
        if (unresolvedImports(result.code, id, false, imports)) incomplete = true;
        const module = await environment.moduleGraph.getModuleById(id) || await environment.moduleGraph.getModuleByUrl(id);
        if (!module) { incomplete = true; return; }
        if (module.file && !dependencyFile(module.file)) {
          try {
            files.add(relative(module.file));
            if (unresolvedImports(readSource(module.file), module.file, true, imports)) incomplete = true;
          } catch { incomplete = true; }
        }
        // Plugins may emit already-transformed SSR loader calls that never
        // populate importedModules. Resolve literal AST imports through the
        // configured resolver rather than assuming the graph observed them.
        for (const specifier of imports) {
          if (specifier.startsWith('node:') || builtinModules.includes(specifier)) continue;
          try {
            const child = await environment.pluginContainer.resolveId(specifier, id, legacy ? { ssr: true } : undefined);
            if (!child?.id || child.id.startsWith('\0')) { incomplete = true; continue; }
            if (dependencyFile(child.id)) continue;
            if (child.external) { incomplete = true; continue; }
            await visit(child.id);
          } catch { incomplete = true; }
        }
        for (const child of module.importedModules) {
          if ((child.file && dependencyFile(child.file)) || (child.id && dependencyFile(child.id))) continue;
          if (child.id && (child.id.startsWith('node:') || builtinModules.includes(child.id))) continue;
          if (!child.id || child.id.startsWith('\0')) { incomplete = true; continue; }
          try { await visit(child.id); } catch { incomplete = true; }
        }
      };
      for (const entry of entries) {
        try { await visit(entry); } catch { incomplete = true; }
      }
      return { files, incomplete };
    };
    for (const project of vitest.projects) {
      // Vite's configured loader supplies config dependencies; transforms supply
      // the recursive setup/globalSetup closure, including aliased helpers.
      const entries = [];
      for (const file of [project.vite.config.configFile, ...project.vite.config.configFileDependencies || []]) {
        if (typeof file !== 'string') continue;
        try {
          const resolved = path.resolve(project.config.root, file);
          global.add(relative(resolved));
          entries.push(resolved);
        } catch { if (!dependencyFile(file)) global.add('*'); }
      }
      for (const file of [...project.config.setupFiles || [], ...project.config.globalSetup || []]) {
        if (typeof file !== 'string') { global.add('*'); continue; }
        const entry = path.resolve(project.config.root, file);
        try { global.add(relative(entry)); } catch { global.add('*'); }
        entries.push(entry);
      }
      const startup = await closure(project, entries);
      for (const file of startup.files) global.add(file);
      if (startup.incomplete) global.add('*');
    }
    for (const spec of specs) {
      const result = await closure(spec.project, [spec.moduleId]);
      scopes.push({ file: relative(spec.moduleId), projectName: spec.project.name, dependencies: [...result.files].sort(), incomplete: result.incomplete });
    }
    await closeChecked();
    await cleanBundlerDirectory();
    protocolWrite(JSON.stringify({ schema: 1, version: pkg.version, viteVersion: vite.version, scopes, global: [...global].sort() }) + '\n');
  } else if (input.mode === 'collect') {
    // Collection executes imports, setup and global setup, never test bodies.
    // Its complete case list is still unqualified native influence evidence.
    if (legacy) await vitest.init();
    else await vitest.standalone();
    const result = await vitest.collectTests(specs);
    if (result.unhandledErrors.length) throw new Error('collection errors');
    const files = [], cases = [], collected = new Set();
    for (const module of result.testModules) {
      const file = relative(module.moduleId), project = module.project.name;
      const id = `${input.workspace}:${project}:${file}`;
      if (!seen.has(id) || collected.has(id) || !['queued', 'pending', 'passed', 'skipped'].includes(module.state())) throw new Error('invalid collection module');
      collected.add(id);
      files.push({ file, project });
      const counts = new Map();
      for (const test of module.children.allTests()) {
        if (!test.fullName) throw new Error('empty collection case');
        const occurrence = counts.get(test.fullName) || 0;
        counts.set(test.fullName, occurrence + 1);
        cases.push({ file, project, test: JSON.stringify([test.fullName, occurrence]) });
        if (cases.length > 100000) throw new Error('case inventory limit');
      }
    }
    if (collected.size !== seen.size || !cases.length) throw new Error('incomplete collection');
    // Teardown can fail after cases exist. Emit evidence only after successful
    // close and require a successful process status independently in Go.
    await closeChecked();
    await cleanBundlerDirectory();
    protocolWrite(JSON.stringify({ schema: 1, version: pkg.version, viteVersion: vite.version, files, cases }) + '\n');
  } else if (input.mode === 'run' || input.mode === 'observe') {
    let selected = specs;
    if (input.selected !== null) {
      const wanted = new Set(input.selected);
      if (!wanted.size || wanted.size !== input.selected.length || [...wanted].some(id => !seen.has(id))) throw new Error('invalid selected inventory');
      selected = specs.filter(spec => wanted.has(identity(spec)));
    }
    // These version-specific APIs initialize reporters without changing scope.
    if (legacy) await vitest.init();
    else await vitest.standalone();
    const result = await vitest.runTestSpecifications(selected, input.selected === null);
    const files = [], tests = [];
    for (const module of result.testModules) {
      const file = relative(module.moduleId), project = module.project.name;
      files.push({ file, project, action: module.state() });
      const counts = new Map();
      for (const test of module.children.allTests()) {
        const occurrence = counts.get(test.fullName) || 0;
        counts.set(test.fullName, occurrence + 1);
        tests.push({ file, project, test: JSON.stringify([test.fullName, occurrence]), action: test.result().state });
      }
    }
    const errors = result.unhandledErrors.length;
    await closeChecked();
    await cleanBundlerDirectory();
    protocolWrite(JSON.stringify({ schema: 1, complete: true, errors, files, tests }) + '\n');
    if (errors || files.some(item => item.action === 'failed') || tests.some(item => item.action === 'failed')) process.exitCode = 1;
  } else throw new Error('unsupported bridge mode');
} catch {
  process.stderr.write('Hayaku Vitest native bridge failed\n');
  process.exitCode = 2;
} finally {
  if (vitest) await vitest.close();
}
