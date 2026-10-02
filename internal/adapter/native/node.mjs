import {run} from 'node:test';
import {writeFileSync} from 'node:fs';

const request = JSON.parse(process.argv[2]);
const options = {files: request.files, concurrency: request.options['--test-concurrency'] ?? 1};
if (request.options['--test-timeout']) options.timeout = request.options['--test-timeout'];
if (request.options['--test-name-pattern']) options.testNamePatterns = request.options['--test-name-pattern'];
if (request.options['--test-skip-pattern']) options.testSkipPatterns = request.options['--test-skip-pattern'];
if (request.mode === 'discover') options.testNamePatterns = 'a^';
const files = new Map(request.files.map((file, index) => [file, request.selectors[index]]));
const summaries = new Map();
const tests = [];
let globalSummary;
let records = 0;
for await (const event of run(options)) {
  if (++records > 200000) throw new Error('native event limit exceeded');
  const data = event.data;
  if (event.type === 'test:summary') {
    if (data.file) {
      if (!files.has(data.file) || summaries.has(data.file)) throw new Error('invalid native file summary');
      summaries.set(data.file, data.success);
    } else {
      if (globalSummary) throw new Error('duplicate native run summary');
      globalSummary = data;
    }
  }
  if (request.mode === 'run' && (event.type === 'test:pass' || event.type === 'test:fail')) {
    if (!files.has(data.file)) throw new Error('unexpected native test file');
    // Native file wrapper outcomes are represented by the file summary.
    if (data.name === data.file && data.nesting === 0) continue;
    let action = event.type === 'test:fail' ? 'fail' : 'pass';
    if ((data.skip !== undefined && data.skip !== false) || (data.todo !== undefined && data.todo !== false)) action = 'skip';
    tests.push({selector: files.get(data.file), test: JSON.stringify([data.nesting, data.name, data.testNumber]), action});
    if (tests.length > 100000) throw new Error('native case limit exceeded');
  }
}
const complete = Boolean(globalSummary) && summaries.size === files.size;
const success = complete && globalSummary.success && [...summaries.values()].every(Boolean);
const payload = request.mode === 'discover'
  ? {schema: 1, runner: 'node-test', version: request.version, complete: complete && success,
     items: request.files.map((file, i) => ({selector: request.selectors[i], path: file, tests: []}))}
  : {schema: 1, runner: 'node-test', version: request.version, complete,
     errors: globalSummary?.counts?.cancelled ?? 1,
     units: [...summaries].map(([file, passed]) => ({selector: files.get(file), action: passed ? 'pass' : 'fail'})), tests};
const serialized = JSON.stringify(payload);
if (Buffer.byteLength(serialized) > 16 * 1024 * 1024) throw new Error('native metadata limit exceeded');
writeFileSync(request.output, serialized, {mode: 0o600, flag: 'wx'});
if (!success) process.exitCode = 1;
