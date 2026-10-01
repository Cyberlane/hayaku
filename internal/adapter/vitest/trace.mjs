// Observational only. Node hooks are not a complete input or security boundary.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { syncBuiltinESMExports } from 'node:module';
import childProcess from 'node:child_process';
import net from 'node:net';
import http from 'node:http';
import https from 'node:https';
import dgram from 'node:dgram';
import workerThreads from 'node:worker_threads';
import crypto from 'node:crypto';

const marker = Symbol.for('hayaku.trace');
if (!process[marker]) {
  const root = process.env.HAYAKU_TRACE_ROOT;
  const output = process.env.HAYAKU_TRACE_OUTPUT;
  if (!root || !output || !path.isAbsolute(root) || !path.isAbsolute(output)) throw new Error('Hayaku runtime observation setup missing');
  const append = fs.appendFileSync.bind(fs);
  const stat = fs.statSync.bind(fs);
  const realpath = fs.realpathSync.bind(fs);
  const lstat = fs.lstatSync.bind(fs);
  const rootPath = path.resolve(root);
  const seen = new Set();
  let count = 0;
  let bytes = 0;
  let writing = false;
  const appendRecord = line => {
    writing = true;
    try { append(output, line + '\n', { mode: 0o600 }); bytes += Buffer.byteLength(line) + 1; } finally { writing = false; }
  };
  const owner = () => {
    const file = globalThis.__vitest_worker__?.filepath;
    if (typeof file !== 'string') return '*';
    const relative = path.relative(rootPath, path.resolve(file)).split(path.sep).join('/');
    return relative && relative !== '..' && !relative.startsWith('../') && !path.isAbsolute(relative) ? relative : '*';
  };
  const write = record => {
    const line = JSON.stringify({ schema: 1, ...record });
    if (seen.has(line)) return;
    // Workers append to one sidecar. Refresh its current size in addition to
    // the local budget; the diagnostic runner serializes worker execution.
    try { bytes = Math.max(bytes, stat(output).size); } catch (error) { if (error.code !== 'ENOENT') throw error; }
    if (count >= 99999 || bytes + Buffer.byteLength(line) + 257 > 32 * 1024 * 1024 || Buffer.byteLength(line) > 32767) {
      if (count < 100000 && bytes + 256 <= 32 * 1024 * 1024) appendRecord(JSON.stringify({ schema: 1, type: 'gap', code: 'record-limit', owner: owner() }));
      count = 100000;
      return;
    }
    count++;
    seen.add(line);
    appendRecord(line);
  };
  const setup = () => write({ type: 'setup', owner: owner() });
  const gap = code => { if (writing) return; setup(); write({ type: 'gap', code, owner: owner() }); };
  const observe = (operation, value) => {
    if (writing) return;
    setup();
    if (typeof value === 'number' || value && typeof value === 'object' && typeof value.fd === 'number') { gap('file-descriptor'); return; }
    if (value instanceof URL) {
      if (value.protocol !== 'file:') { gap('unsupported-path'); return; }
      try { value = fileURLToPath(value); } catch { gap('unsupported-path'); return; }
    }
    if (typeof value !== 'string' || value.includes('\0')) { gap('unsupported-path'); return; }
    const absolute = path.resolve(value);
    const relative = path.relative(rootPath, absolute).split(path.sep).join('/') || '.';
    if (relative.length > 16384 || /[\0\r\n\\:]/.test(relative)) { gap('unsupported-path'); return; }
    if (relative === '..' || relative.startsWith('../') || path.isAbsolute(relative)) { gap('outside-snapshot'); return; }
    if (relative === '.' && (operation === 'read' || operation === 'readlink')) { gap('unsupported-path'); return; }
    // Bound installed dependency bytes are already global execution inputs.
    if (relative.split('/').includes('node_modules')) return;
    let ancestor = absolute;
    for (;;) {
      try {
        const resolved = realpath(ancestor);
        const rel = path.relative(rootPath, resolved);
        if (rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel)) { gap('outside-snapshot'); return; }
        // An alias is influenced by its target and every symlink parent. The
        // diagnostic protocol does not encode that complete chain, so retain
        // uncertainty even when the final target stays inside the snapshot.
        if (resolved !== ancestor) gap('path-resolution');
        break;
      } catch (error) {
        if (error.code !== 'ENOENT' && error.code !== 'ENOTDIR') { gap('path-resolution'); break; }
        // A dangling symlink cannot be normalized through realpath. Preserve
        // explicit uncertainty rather than mistaking its target for absence.
        try { if (lstat(ancestor).isSymbolicLink()) { gap('path-resolution'); break; } } catch {}
        const parent = path.dirname(ancestor);
        if (parent === ancestor) { gap('path-resolution'); break; }
        ancestor = parent;
      }
    }
    write({ type: 'observation', operation, path: relative, owner: owner() });
  };
  const wrap = (object, name, before) => {
    const original = object[name];
    if (typeof original !== 'function') return;
    object[name] = function (...args) { before(args); return Reflect.apply(original, this, args); };
    // Preserve native aliases and custom-promisification symbols used by Vite
    // and Node internals. Alias interception remains an explicit limitation.
    Object.setPrototypeOf(object[name], original);
  };
  for (const [operation, names] of [
    ['read', ['readFile', 'open', 'createReadStream']],
    ['metadata', ['stat', 'lstat', 'realpath']],
    ['exists', ['access', 'exists']],
    ['directory', ['readdir', 'opendir']],
    ['readlink', ['readlink']],
  ]) {
    for (const name of names) {
      wrap(fs, name, args => observe(operation, args[0]));
      wrap(fs, `${name}Sync`, args => observe(operation, args[0]));
      wrap(fs.promises, name, args => observe(operation, args[0]));
    }
  }
  for (const name of ['read', 'readv', 'fstat']) {
    wrap(fs, name, () => gap('file-descriptor'));
    wrap(fs, `${name}Sync`, () => gap('file-descriptor'));
  }
  for (const name of ['writeFile', 'appendFile', 'truncate', 'unlink', 'rename', 'mkdir', 'rmdir', 'rm', 'symlink', 'link', 'chmod', 'chown', 'utimes', 'createWriteStream']) {
    wrap(fs, name, () => gap('filesystem-write'));
    wrap(fs, `${name}Sync`, () => gap('filesystem-write'));
    wrap(fs.promises, name, () => gap('filesystem-write'));
  }
  for (const name of ['exec', 'execFile', 'spawn', 'fork', 'execSync', 'execFileSync', 'spawnSync']) wrap(childProcess, name, () => gap('subprocess'));
  for (const [object, names] of [[net, ['connect', 'createConnection', 'createServer']], [http, ['request', 'get', 'createServer']], [https, ['request', 'get', 'createServer']], [dgram, ['createSocket']]]) {
    for (const name of names) wrap(object, name, () => gap('network'));
  }
  wrap(globalThis, 'fetch', () => gap('network'));
  wrap(process, 'dlopen', () => gap('native-addon'));
  wrap(Date, 'now', () => gap('clock'));
  const NativeDate = Date;
  globalThis.Date = new Proxy(NativeDate, {
    construct(target, args, newTarget) { if (!args.length) gap('clock'); return Reflect.construct(target, args, newTarget); },
    apply(target, receiver, args) { gap('clock'); return Reflect.apply(target, receiver, args); },
  });
  wrap(Math, 'random', () => gap('random'));
  for (const name of ['randomBytes', 'randomFill', 'randomFillSync', 'randomInt', 'randomUUID']) wrap(crypto, name, () => gap('random'));
  const Worker = workerThreads.Worker;
  workerThreads.Worker = new Proxy(Worker, { construct(target, args, newTarget) { gap('worker'); return Reflect.construct(target, args, newTarget); } });
  process[marker] = { setup };
  syncBuiltinESMExports();
  setup();
}
// Vitest may share a worker process across files; each setup invocation proves
// only that this module was reached for the current file, never full coverage.
process[marker].setup();
