'use strict';

// Only the pinned parser/emitter is executed. User modules are parsed, never
// loaded. This source candidate does not certify the framework/runtime TCB.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const MAX_NODES = 100000, MAX_COST = 100000, MAX_DEPTH = 64;
const fail = () => { throw new Error('source outside pure Node candidate'); };
const hash = value => crypto.createHash('sha256').update(JSON.stringify(value)).digest('hex');
const text = value => typeof value === 'string' && value.length > 0 && Buffer.byteLength(value) <= 256 && Buffer.from(value, 'utf8').toString('utf8') === value && !/[\x00-\x1f\x7f]/.test(value);
const name = value => /^[A-Za-z_$][A-Za-z0-9_$]{0,63}$/.test(value) && !new Set(['eval', 'Function', 'require', 'process', 'global', 'globalThis', 'Date', 'Math', 'Object', 'Reflect', 'Proxy', 'Promise', 'Symbol', 'BigInt', 'undefined', 'NaN', 'Infinity', 'constructor', '__proto__', 'prototype', 'arguments', 'await', 'yield', 'implements', 'interface', 'package', 'private', 'protected', 'public', 'static', 'let', 'break', 'case', 'catch', 'class', 'const', 'continue', 'debugger', 'default', 'delete', 'do', 'else', 'enum', 'export', 'extends', 'false', 'finally', 'for', 'function', 'if', 'import', 'in', 'instanceof', 'new', 'null', 'return', 'super', 'switch', 'this', 'throw', 'true', 'try', 'typeof', 'var', 'void', 'while', 'with']).has(value);

function readBounded(filename, limit) {
  const before = fs.lstatSync(filename, { bigint: true });
  if (!before.isFile() || before.isSymbolicLink() || before.size < 0n || before.size > BigInt(limit)) fail();
  const fd = fs.openSync(filename, fs.constants.O_RDONLY | fs.constants.O_NONBLOCK | (fs.constants.O_NOFOLLOW || 0));
  try {
    const same = info => info.isFile() && info.dev === before.dev && info.ino === before.ino && info.mode === before.mode && info.size === before.size && info.mtimeNs === before.mtimeNs && info.ctimeNs === before.ctimeNs;
    if (!same(fs.fstatSync(fd, { bigint: true }))) fail();
    const bytes = Buffer.alloc(Number(before.size) + 1);
    let used = 0;
    while (used < bytes.length) {
      const n = fs.readSync(fd, bytes, used, bytes.length - used, null);
      if (!n) break;
      used += n;
    }
    if (used !== Number(before.size) || !same(fs.fstatSync(fd, { bigint: true })) || !same(fs.lstatSync(filename, { bigint: true }))) fail();
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(0, used));
  } finally { fs.closeSync(fd); }
}

function certify(ts, files, tests, emitted) {
  const modules = new Map(), visiting = new Set(), completed = new Set();
  let nodeCount = 0, totalCost = 0;
  const S = ts.SyntaxKind;
  const tick = depth => { if (++nodeCount > MAX_NODES || depth > MAX_DEPTH) fail(); };
  const type = node => {
    if (!node) return null;
    if (node.kind === S.NumberKeyword) return 'number';
    if (node.kind === S.BooleanKeyword) return 'boolean';
    fail();
  };
  const id = node => { if (!node || !ts.isIdentifier(node) || !name(node.text)) fail(); return node.text; };
  const modifiers = node => {
    const result = node.modifiers || [];
    if (result.length > 1 || result.some(m => m.kind !== S.ExportKeyword)) fail();
    return result.length === 1;
  };
  const resolve = (file, spec) => {
    if (!/^\.\.?\/[A-Za-z0-9_./-]+\.(?:js|ts)$/.test(spec)) fail();
    const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(file), spec));
    if (resolved.startsWith('../') || resolved.startsWith('/')) fail();
    // Framework TS remapping can precede a real .js file. Ambiguous compiler
    // and framework choices must retain the complete original scope.
    if (resolved.endsWith('.js') && Object.hasOwn(files, resolved) && Object.hasOwn(files, resolved.slice(0, -3) + '.ts')) fail();
    if (Object.hasOwn(files, resolved)) return resolved;
    if (resolved.endsWith('.js') && Object.hasOwn(files, resolved.slice(0, -3) + '.ts')) return resolved.slice(0, -3) + '.ts';
    fail();
  };
  for (const [file, source] of Object.entries(files).sort()) {
    const tsInput = !emitted && file.endsWith('.ts');
    const ast = ts.createSourceFile(file, source, ts.ScriptTarget.ES2022, true, tsInput ? ts.ScriptKind.TS : ts.ScriptKind.JS);
    if (ast.parseDiagnostics.length || ast.isDeclarationFile || ast.referencedFiles.length || ast.typeReferenceDirectives.length || ast.libReferenceDirectives.length || ast.hasNoDefaultLib || ast.pragmas?.size) fail();
    const scan = (node, depth) => { tick(depth); ts.forEachChild(node, child => scan(child, depth + 1)); };
    scan(ast, 0);
    const mod = { file, ast, env: new Map(), imports: [], functions: [], cases: [], exports: new Map(), canonical: [] };
    const bind = (key, value) => { if (mod.env.has(key)) fail(); mod.env.set(key, value); };
    for (const statement of ast.statements) {
      if (ts.isImportDeclaration(statement)) {
        if (statement.modifiers?.length || statement.attributes || !ts.isStringLiteral(statement.moduleSpecifier)) fail();
        const clause = statement.importClause;
        if (!clause || clause.isTypeOnly || clause.name || !clause.namedBindings || !ts.isNamedImports(clause.namedBindings)) fail();
        const spec = statement.moduleSpecifier.text;
        const target = spec === 'vitest' ? 'vitest' : resolve(file, spec);
        // Importing an entry can register its cases under another file's lifecycle.
        if (target !== 'vitest' && tests.includes(target)) fail();
        if (!clause.namedBindings.elements.length) fail();
        for (const item of clause.namedBindings.elements) {
          if (item.isTypeOnly) fail();
          const local = id(item.name), imported = id(item.propertyName || item.name);
          if (target === 'vitest' && (!tests.includes(file) || !['test', 'expect'].includes(imported))) fail();
          bind(local, target === 'vitest' ? { kind: imported, key: 'vitest:' + imported } : { kind: 'import', target, imported });
          mod.imports.push([target, imported, local]);
        }
      } else if (ts.isVariableStatement(statement)) {
        const exported = modifiers(statement);
        if ((statement.declarationList.flags & ts.NodeFlags.BlockScoped) !== ts.NodeFlags.Const || statement.declarationList.declarations.length !== 1) fail();
        const declaration = statement.declarationList.declarations[0], key = id(declaration.name);
        if (declaration.exclamationToken || !declaration.initializer) fail();
        const literal = declaration.initializer;
        let primitive;
        if (ts.isNumericLiteral(literal) && /^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$/.test(literal.text) && Number(literal.text) <= 1000000) primitive = ['number', Number(literal.text)];
        else if ([S.TrueKeyword, S.FalseKeyword].includes(literal.kind)) primitive = ['boolean', literal.kind === S.TrueKeyword];
        else fail();
        if (declaration.type && (emitted || !tsInput || type(declaration.type) !== primitive[0])) fail();
        const symbol = { kind: 'value', type: primitive[0], key: file + ':' + key, primitive };
        bind(key, symbol); if (exported) mod.exports.set(key, symbol);
        mod.canonical.push(['const', key, exported, primitive]);
      } else if (ts.isFunctionDeclaration(statement)) {
        const exported = modifiers(statement), key = id(statement.name);
        if (statement.asteriskToken || statement.typeParameters?.length || !statement.body || !ts.isBlock(statement.body)) fail();
        const parameters = statement.parameters.map(p => {
          const parameter = id(p.name);
          if (p.modifiers?.length || p.dotDotDotToken || p.questionToken || p.initializer || (p.type && (emitted || !tsInput || type(p.type) !== 'number'))) fail();
          return parameter;
        });
        if (parameters.length > 8 || new Set(parameters).size !== parameters.length) fail();
        if (statement.type && (emitted || !tsInput)) fail();
        const symbol = { kind: 'function', key: file + ':' + key, mod, statement, parameters, expected: type(statement.type), exported };
        bind(key, symbol); mod.functions.push(symbol); if (exported) mod.exports.set(key, symbol);
      } else if (ts.isExpressionStatement(statement) && tests.includes(file)) mod.cases.push(statement.expression);
      else fail();
    }
    modules.set(file, mod);
  }
  function link(mod) {
    if (completed.has(mod.file)) return;
    if (visiting.has(mod.file)) fail();
    visiting.add(mod.file); if (visiting.size > MAX_DEPTH) fail();
    for (const [key, value] of mod.env) if (value.kind === 'import') {
      const dependency = modules.get(value.target); if (!dependency) fail();
      link(dependency);
      const original = dependency.exports.get(value.imported); if (!original) fail();
      mod.env.set(key, original);
    }
    visiting.delete(mod.file); completed.add(mod.file);
  }
  for (const mod of modules.values()) link(mod);

  function expression(node, env, depth = 0) {
    if (depth > MAX_DEPTH) fail();
    if (ts.isNumericLiteral(node) && /^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$/.test(node.text) && Number(node.text) <= 1000000) return { type: 'number', cost: 1, tree: ['number', Number(node.text)] };
    if ([S.TrueKeyword, S.FalseKeyword].includes(node.kind)) return { type: 'boolean', cost: 1, tree: ['boolean', node.kind === S.TrueKeyword] };
    if (ts.isIdentifier(node)) {
      const symbol = env.get(id(node));
      if (!symbol || symbol.kind !== 'value') fail();
      return { type: symbol.type, cost: 1, tree: ['value', symbol.key] };
    }
    if (ts.isParenthesizedExpression(node)) return expression(node.expression, env, depth + 1);
    if (ts.isPrefixUnaryExpression(node)) {
      const operand = expression(node.operand, env, depth + 1);
      if (!((node.operator === S.MinusToken && operand.type === 'number') || (node.operator === S.ExclamationToken && operand.type === 'boolean'))) fail();
      return { type: operand.type, cost: operand.cost + 1, tree: ['unary', node.operator, operand.tree] };
    }
    if (ts.isBinaryExpression(node)) {
      const left = expression(node.left, env, depth + 1), right = expression(node.right, env, depth + 1), op = node.operatorToken.kind;
      if (left.type !== right.type) fail();
      const numeric = [S.PlusToken, S.MinusToken, S.AsteriskToken, S.SlashToken, S.PercentToken];
      const comparison = [S.LessThanToken, S.GreaterThanToken, S.LessThanEqualsToken, S.GreaterThanEqualsToken];
      const equality = [S.EqualsEqualsEqualsToken, S.ExclamationEqualsEqualsToken];
      const boolean = [S.AmpersandAmpersandToken, S.BarBarToken];
      if (!(equality.includes(op) || (left.type === 'number' && [...numeric, ...comparison].includes(op)) || (left.type === 'boolean' && boolean.includes(op)))) fail();
      const cost = left.cost + right.cost + 1; if (cost > MAX_COST) fail();
      return { type: numeric.includes(op) ? 'number' : 'boolean', cost, tree: ['binary', op, left.tree, right.tree] };
    }
    if (ts.isConditionalExpression(node)) {
      const condition = expression(node.condition, env, depth + 1), yes = expression(node.whenTrue, env, depth + 1), no = expression(node.whenFalse, env, depth + 1);
      const cost = condition.cost + yes.cost + no.cost + 1;
      if (condition.type !== 'boolean' || yes.type !== no.type || cost > MAX_COST) fail();
      return { type: yes.type, cost, tree: ['conditional', condition.tree, yes.tree, no.tree] };
    }
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && !node.questionDotToken && !node.typeArguments?.length) {
      const symbol = env.get(id(node.expression));
      if (!symbol || symbol.kind !== 'function' || node.arguments.length !== symbol.parameters.length) fail();
      const fn = functionBody(symbol), args = node.arguments.map(a => expression(a, env, depth + 1));
      if (args.some(a => a.type !== 'number')) fail();
      const cost = fn.cost + args.reduce((n, a) => n + a.cost, 1); if (cost > MAX_COST) fail();
      return { type: fn.type, cost, tree: ['call', symbol.key, args.map(a => a.tree)] };
    }
    fail();
  }
  function local(statement, env, prefix, allowTypes) {
    if (!ts.isVariableStatement(statement) || statement.modifiers?.length || (statement.declarationList.flags & ts.NodeFlags.BlockScoped) !== ts.NodeFlags.Const || statement.declarationList.declarations.length !== 1) fail();
    const declaration = statement.declarationList.declarations[0], key = id(declaration.name);
    if (env.has(key) || declaration.exclamationToken || !declaration.initializer) fail();
    const value = expression(declaration.initializer, env);
    if (declaration.type && (!allowTypes || type(declaration.type) !== value.type)) fail();
    env.set(key, { kind: 'value', key: prefix + ':' + key, type: value.type });
    return { cost: value.cost, tree: ['local', key, value.tree] };
  }
  let functionDepth = 0;
  function functionBody(symbol) {
    if (symbol.checked) return symbol.checked;
    if (symbol.checking) fail();
    symbol.checking = true; if (++functionDepth > MAX_DEPTH) fail();
    const env = new Map(symbol.mod.env), trees = [];
    for (const parameter of symbol.parameters) {
      if (env.has(parameter)) fail();
      env.set(parameter, { kind: 'value', key: symbol.key + ':' + parameter, type: 'number' });
    }
    let result, cost = 0;
    const statements = symbol.statement.body.statements;
    if (!statements.length || statements.length > 128) fail();
    for (let i = 0; i < statements.length; i++) {
      const statement = statements[i];
      if (i === statements.length - 1 && ts.isReturnStatement(statement) && statement.expression) {
        result = expression(statement.expression, env); cost += result.cost; trees.push(['return', result.tree]);
      } else { const value = local(statement, env, symbol.key, !emitted && symbol.mod.file.endsWith('.ts')); cost += value.cost; trees.push(value.tree); }
      if (cost > MAX_COST) fail();
    }
    if (!result || (symbol.expected && result.type !== symbol.expected)) fail();
    symbol.checking = false; functionDepth--; symbol.checked = { type: result.type, cost, tree: ['function', symbol.key, symbol.exported, symbol.parameters, trees] };
    return symbol.checked;
  }
  const cases = [], semantic = [];
  for (const mod of modules.values()) {
    const functions = mod.functions.map(fn => functionBody(fn).tree);
    const caseTrees = [], labels = new Set();
    for (const call of mod.cases) {
      if (!ts.isCallExpression(call) || !ts.isIdentifier(call.expression) || call.questionDotToken || call.typeArguments?.length || mod.env.get(id(call.expression))?.kind !== 'test' || call.arguments.length !== 2) fail();
      const [label, callback] = call.arguments;
      if (!ts.isStringLiteral(label) || !text(label.text) || labels.has(label.text) || !ts.isArrowFunction(callback) || callback.modifiers?.length || callback.typeParameters?.length || callback.parameters.length || callback.type || !ts.isBlock(callback.body)) fail();
      labels.add(label.text);
      const env = new Map(mod.env), body = [];
      if (!callback.body.statements.length || callback.body.statements.length > 128) fail();
      for (const statement of callback.body.statements) {
        let result;
        if (ts.isVariableStatement(statement)) result = local(statement, env, mod.file + ':' + label.text, !emitted && mod.file.endsWith('.ts'));
        else {
          if (!ts.isExpressionStatement(statement)) fail();
          const matcher = statement.expression;
          if (!ts.isCallExpression(matcher) || matcher.questionDotToken || matcher.typeArguments?.length || !ts.isPropertyAccessExpression(matcher.expression) || matcher.expression.questionDotToken || matcher.arguments.length !== 1) fail();
          const property = matcher.expression, expect = property.expression;
          if (!['toBe', 'toBeGreaterThan', 'toBeLessThan'].includes(id(property.name)) || !ts.isCallExpression(expect) || expect.questionDotToken || expect.typeArguments?.length || !ts.isIdentifier(expect.expression) || mod.env.get(id(expect.expression))?.kind !== 'expect' || env.get(id(expect.expression)) !== mod.env.get(id(expect.expression)) || expect.arguments.length !== 1) fail();
          const actual = expression(expect.arguments[0], env), expected = expression(matcher.arguments[0], env);
          if (actual.type !== expected.type || (property.name.text !== 'toBe' && actual.type !== 'number')) fail();
          result = { cost: actual.cost + expected.cost + 1, tree: ['assert', property.name.text, actual.tree, expected.tree] };
        }
        totalCost += result.cost; if (totalCost > MAX_COST) fail(); body.push(result.tree);
      }
      cases.push({ file: mod.file, label: label.text }); caseTrees.push([label.text, body]);
      if (cases.length > 1000) fail();
    }
    if (tests.includes(mod.file) && !mod.cases.length) fail();
    semantic.push([mod.file, mod.imports, mod.canonical, functions, caseTrees]);
  }
  if (!cases.length) fail();
  const dependencies = Object.fromEntries([...modules].map(([file, mod]) =>
    [file, [...new Set(mod.imports.map(item => item[0]).filter(target => target !== 'vitest'))].sort()]));
  return { cases, semantic, dependencies, cost: totalCost, nodes: nodeCount };
}

try {
  const input = JSON.parse(readBounded(process.argv[2], 16 << 20));
  if (input.schema !== 1 || input.version !== 'node-pure-source-candidate-v1' || !Array.isArray(input.tests) || !input.tests.length || input.tests.length > 128 || !Array.isArray(input.sources) || !input.sources.length || input.sources.length > 128) fail();
  const ts = require(input.compiler);
  if (ts.version !== '6.0.2') fail();
  const files = Object.create(null);
  let bytes = 0;
  for (const source of input.sources) {
    if (typeof source.file !== 'string' || Object.hasOwn(files, source.file) || !/^[A-Za-z0-9_./-]+\.(?:js|ts)$/.test(source.file) || source.file.startsWith('/') || source.file.split('/').some(p => !p || p === '.' || p === '..')) fail();
    files[source.file] = readBounded(source.path, 256 << 10); bytes += Buffer.byteLength(files[source.file]); if (bytes > 8 << 20) fail();
  }
  if (new Set(input.tests).size !== input.tests.length || input.tests.some(file => !Object.hasOwn(files, file))) fail();
  const original = certify(ts, files, input.tests, false);
  const options = { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, isolatedModules: true, verbatimModuleSyntax: true, removeComments: true, newLine: ts.NewLineKind.LineFeed };
  const emitted = Object.create(null);
  let emittedBytes = 0;
  for (const [file, source] of Object.entries(files)) {
    if (file.endsWith('.ts')) {
      const output = ts.transpileModule(source, { fileName: file, compilerOptions: options, reportDiagnostics: true });
      if ((output.diagnostics || []).some(d => d.category === ts.DiagnosticCategory.Error)) fail();
      emitted[file] = output.outputText;
    } else emitted[file] = source;
    emittedBytes += Buffer.byteLength(emitted[file]); if (emittedBytes > 8 << 20) fail();
  }
  const second = certify(ts, emitted, input.tests, true);
  if (JSON.stringify(original.semantic) !== JSON.stringify(second.semantic) || JSON.stringify(original.cases) !== JSON.stringify(second.cases)) fail();
  const result = { schema: 1, version: input.version, parserVersion: ts.version, nodeVersion: process.versions.node, cases: original.cases, dependencies: original.dependencies, semanticDigest: hash(original.semantic), emittedDigest: hash(emitted), emitterDigest: hash(options), cost: original.cost, nodes: original.nodes };
  fs.writeSync(1, JSON.stringify(result) + '\n');
} catch {
  fs.writeSync(2, 'Node source certification rejected\n');
  process.exitCode = 125;
}
