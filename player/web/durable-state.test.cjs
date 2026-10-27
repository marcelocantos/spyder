// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
// Run: node --test player/web/durable-state.test.cjs
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(__dirname + '/durable-state.js', 'utf8');
const root = '/spyder-state';

function browser(backing, holds = []) {
  const files = new Map();
  const dirs = new Set();
  const FS = {
    mkdir(path) {
      if (dirs.has(path)) throw Error('EEXIST');
      dirs.add(path);
    },
    mount(type, options, path) { assert.equal(path, root); assert.equal(type, 'IDBFS'); },
    analyzePath(path) { return { exists: dirs.has(path) }; },
    writeFile(path, bytes) {
      assert(dirs.has(root + '/games'));
      files.set(path, Uint8Array.from(bytes));
    },
    readFile(path) { return files.get(path); },
    syncfs(populate, cb) {
      if (populate) {
        for (const [path, bytes] of backing) files.set(path, Uint8Array.from(bytes));
        if ([...backing.keys()].some(path => path.startsWith(root + '/games/')))
          dirs.add(root + '/games');
        queueMicrotask(() => cb(null));
      } else {
        holds.push(() => {
          for (const [path, bytes] of files) backing.set(path, Uint8Array.from(bytes));
          cb(null);
        });
      }
    },
  };
  const context = vm.createContext({ console, Uint8Array, Promise });
  vm.runInContext(source, context);
  return { FS, store: context.SpyderDurableState, files };
}

test('first attach has no seed; snapshot sync survives a new browser runtime, scoped by game', async () => {
  const backing = new Map();
  const jobs = [];
  let a = browser(backing, jobs);
  assert.equal(await a.store.init(a.FS, 'IDBFS'), true);
  assert.equal(a.FS.readFile(root + '/games/alpha.db'), undefined);
  assert(a.store.write(a.FS, root + '/games/alpha.db', Uint8Array.of(1, 2)));
  assert(a.store.write(a.FS, root + '/games/beta.db', Uint8Array.of(8)));
  assert.equal(jobs.length, 1, 'one sync at a time');
  jobs.shift()();
  await new Promise(setImmediate);
  assert.equal(jobs.length, 1);
  jobs.shift()();
  await a.store.flush();
  a = browser(backing, jobs); // simulated tab close/reopen
  assert.equal(await a.store.init(a.FS, 'IDBFS'), true);
  assert.deepEqual([...a.FS.readFile(root + '/games/alpha.db')], [1, 2]);
  assert.deepEqual([...a.FS.readFile(root + '/games/beta.db')], [8]);
  assert.equal(a.FS.readFile(root + '/games/gamma.db'), undefined);
});

test('latest wins while a sync is pending; reload waits for populate before reading', async () => {
  const backing = new Map();
  const jobs = [];
  const a = browser(backing, jobs);
  await a.store.init(a.FS, 'IDBFS');
  const path = root + '/games/alpha.db';
  a.store.write(a.FS, path, Uint8Array.of(1));
  a.store.write(a.FS, path, Uint8Array.of(2));
  a.store.write(a.FS, path, Uint8Array.of(3));
  assert.deepEqual([...a.FS.readFile(path)], [1]);
  jobs.shift()();
  await new Promise(setImmediate);
  assert.deepEqual([...a.FS.readFile(path)], [3]);
  jobs.shift()();
  await a.store.flush();
  const b = browser(backing);
  assert.equal(await b.store.init(b.FS, 'IDBFS'), true);
  assert.deepEqual([...b.FS.readFile(path)], [3]);
});

test('failed population never permits writes or an empty seed', async () => {
  const b = browser(new Map());
  b.FS.syncfs = (_, cb) => cb(Error('blocked'));
  assert.equal(await b.store.init(b.FS, 'IDBFS'), false);
  assert.equal(b.store.write(b.FS, root + '/games/alpha.db', Uint8Array.of(1)), false);
});
