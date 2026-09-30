// Private adapter protocol for the explicitly qualified Vitest 4.1.11 API.
// No package download, source-text parser, or related-test CLI is used.
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { builtinModules } from 'node:module';
const input = JSON.parse(process.argv[2]);
const protocolWrite = process.stdout.write.bind(process.stdout);
// Configuration, transforms and workers may log; logs are never protocol data.
process.stdout.write = (...args) => process.stderr.write(...args);
let vitest;
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
  const pkg = JSON.parse(fs.readFileSync(path.join(input.modules, 'vitest/package.json'), 'utf8'));
  if (pkg.version !== '4.1.11') throw new Error('unsupported Vitest API version');
  const api = await import(pathToFileURL(path.join(input.modules, 'vitest/dist/node.js')).href);
  const parsed = api.parseCLI(['vitest', ...input.args]);
  vitest = await api.createVitest('test', { ...parsed.options, watch: false, root: input.cwd, cache: false }, { cacheDir: path.join(path.dirname(process.argv[1]), 'vite-cache') });
  // Reject native modes that alter scope, write acceptance artifacts, or cannot
  // be reconciled by this qualified Node file protocol.
  const config = vitest.config;
  if (config.changed || config.related?.length || config.shard || config.standalone || config.bail || config.testNamePattern || config.tagsFilter?.length || config.coverage?.enabled || config.dangerouslyIgnoreUnhandledErrors || config.update === true || config.update === 'all' || config.update === 'new') throw new Error('unsupported configured Vitest execution mode');
  for (const project of vitest.projects) {
    if (project.config.browser?.enabled || project.config.typecheck?.enabled) throw new Error('unsupported configured Vitest project mode');
  }
  const specs = await vitest.globTestSpecifications(parsed.filter);
  if (!specs.length || specs.length > 50000) throw new Error('empty or oversized inventory');
  const seen = new Set();
  for (const spec of specs) {
    const id = identity(spec);
    if (seen.has(id)) throw new Error('duplicate specification');
    seen.add(id);
  }
  if (input.mode === 'discover') {
    const scopes = [];
    const global = new Set();
    for (const project of vitest.projects) {
      for (const file of [project.vite.config.configFile, ...project.vite.config.configFileDependencies || [], ...project.config.setupFiles || [], ...project.config.globalSetup || []]) {
        if (typeof file === 'string') {
          try { global.add(relative(path.resolve(project.config.root, file))); } catch { if (!dependencyFile(file)) global.add('*'); }
        }
      }
    }
    for (const spec of specs) {
      const files = new Set();
      const visited = new Set();
      let incomplete = false;
      const environment = spec.project.vite.environments.ssr;
      const visit = async id => {
        if (visited.has(id)) return;
        visited.add(id);
        if (visited.size > 50000) throw new Error('graph limit');
        const result = await environment.transformRequest(id);
        if (!result) { incomplete = true; return; }
        const module = await environment.moduleGraph.getModuleById(id) || await environment.moduleGraph.getModuleByUrl(id);
        if (!module) { incomplete = true; return; }
        if (module.file && !dependencyFile(module.file)) {
          try { files.add(relative(module.file)); } catch { incomplete = true; }
        }
        for (const child of module.importedModules) {
          if ((child.file && dependencyFile(child.file)) || (child.id && dependencyFile(child.id))) continue;
          if (child.id && (child.id.startsWith('node:') || builtinModules.includes(child.id))) continue;
          if (!child.id || child.id.startsWith('\0')) { incomplete = true; continue; }
          try { await visit(child.id); } catch { incomplete = true; }
        }
      };
      try { await visit(spec.moduleId); } catch { incomplete = true; }
      scopes.push({ file: relative(spec.moduleId), projectName: spec.project.name, dependencies: [...files].sort(), incomplete });
    }
    await vitest.close(); vitest = undefined;
    await cleanBundlerDirectory();
    protocolWrite(JSON.stringify({ schema: 1, version: pkg.version, scopes, global: [...global].sort() }) + '\n');
  } else if (input.mode === 'run') {
    let selected = specs;
    if (input.selected !== null) {
      const wanted = new Set(input.selected);
      if (!wanted.size || wanted.size !== input.selected.length || [...wanted].some(id => !seen.has(id))) throw new Error('invalid selected inventory');
      selected = specs.filter(spec => wanted.has(identity(spec)));
    }
    // standalone initializes coverage/reporters without changing file selection.
    await vitest.standalone();
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
    await vitest.close(); vitest = undefined;
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
