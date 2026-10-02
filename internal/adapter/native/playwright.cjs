const fs = require('node:fs');
const path = require('node:path');
module.exports = class HayakuReporter {
  constructor() { this.errors = 0; this.begun = false; }
  onBegin(config, suite) {
    if (this.begun) throw new Error('duplicate native begin');
    this.begun = true;
    this.tests = suite.allTests();
    if (this.tests.length > 100000) throw new Error('native case limit exceeded');
  }
  onError() { this.errors++; }
  onEnd(result) {
    const items = new Map();
    for (const test of this.tests ?? []) {
      const file = path.resolve(test.location.file);
      const selector = path.relative(process.env.HAYAKU_NATIVE_CWD, file).split(path.sep).join('/');
      const item = items.get(selector) ?? {selector, path: file, tests: []};
      item.tests.push(test.id);
      items.set(selector, item);
    }
    let payload;
    if (process.env.HAYAKU_NATIVE_MODE === 'discover') {
      payload = {schema: 1, runner: 'playwright', version: process.env.HAYAKU_NATIVE_VERSION,
                 complete: this.begun && this.errors === 0 && result.status === 'passed', items: [...items.values()]};
    } else {
      const tests = [];
      const actions = new Map();
      let complete = this.begun && !['timedout', 'interrupted'].includes(result.status);
      for (const test of this.tests ?? []) {
        const selector = path.relative(process.env.HAYAKU_NATIVE_CWD, path.resolve(test.location.file)).split(path.sep).join('/');
        const outcome = test.outcome();
        let action;
        if (outcome === 'skipped') action = 'skip';
        else if (outcome === 'expected') action = 'pass';
        else if (outcome === 'unexpected' || outcome === 'flaky') action = 'fail';
        else { complete = false; continue; }
        if (test.results.length === 0) { complete = false; continue; }
        tests.push({selector, test: test.id, action});
        const prior = actions.get(selector);
        actions.set(selector, action === 'fail' || prior === 'fail' ? 'fail' : action === 'pass' || prior === 'pass' ? 'pass' : 'skip');
      }
      payload = {schema: 1, runner: 'playwright', version: process.env.HAYAKU_NATIVE_VERSION,
                 complete, errors: this.errors,
                 units: [...actions].map(([selector, action]) => ({selector, action})), tests};
    }
    const serialized = JSON.stringify(payload);
    if (Buffer.byteLength(serialized) > 16 * 1024 * 1024) throw new Error('native metadata limit exceeded');
    fs.writeFileSync(process.env.HAYAKU_NATIVE_OUTPUT, serialized, {mode: 0o600, flag: 'wx'});
  }
  printsToStdio() { return false; }
};
