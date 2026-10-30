// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(__dirname + '/platform.js', 'utf8');
function glass({secure = true, permission = 'granted', motion = true, inset = [4, 12, 6, 8]} = {}) {
  const elements = {}, listeners = {};
  const document = {
    getElementById(id) { return elements[id]; },
    body: {append(el) { if (el.id) elements[el.id] = el; }},
    createElement() { return {style: {}, hidden: false, setAttribute() {}, addEventListener(type, cb) { this[type] = cb; }}; },
    addEventListener(type, cb) { listeners[type] = cb; }, hidden: false,
  };
  const canvas = {requestFullscreen() { calls.push('fullscreen'); return Promise.resolve(); }};
  elements.canvas = canvas;
  const calls = [];
  const context = {document, console: {warn() {}}, isSecureContext: secure, devicePixelRatio: 2,
    DeviceMotionEvent: motion ? {requestPermission() { calls.push('permission'); return Promise.resolve(permission); }} : undefined,
    navigator: {wakeLock: {request() { calls.push('wake'); return Promise.resolve({addEventListener() {}}); }}},
    screen: {orientation: {lock(mode) { calls.push(mode); return Promise.resolve(); }}},
    getComputedStyle() { return {paddingLeft: `${inset[0]}px`, paddingTop: `${inset[1]}px`, paddingRight: `${inset[2]}px`, paddingBottom: `${inset[3]}px`}; },
    addEventListener(type, cb) { listeners[type] = cb; },
  };
  context.globalThis = context;
  vm.runInNewContext(source, context);
  return {state: context.spyderPlatform, elements, listeners, calls};
}
test('motion is absent before permission and before finite sample; denial and insecure stay absent', async () => {
  for (const options of [{permission:'denied'}, {secure:false}, {}]) {
    const g = glass(options);
    g.state.configure(1, 0, 0);
    assert.equal(g.state.takeMotion(0), null);
    if (!g.elements['motion-permission'].hidden) await g.elements['motion-permission'].click();
    if (options.permission === 'denied' || options.secure === false) {
      assert.equal(g.state.state.motion, null);
      continue;
    }
    g.listeners.devicemotion({accelerationIncludingGravity:{x:NaN, y:1, z:2}});
    assert.equal(g.state.takeMotion(0), null);
    g.listeners.devicemotion({accelerationIncludingGravity:{x:1, y:-2, z:9.8}});
    assert.deepEqual(Array.from(g.state.takeMotion(0).values), [1,-2,9.8]);
    assert.equal(g.state.takeMotion(1), null);
  }
});
test('policy gesture and safe insets map to physical pixels', async () => {
  const g = glass();
  g.state.configure(0, 1, 3);
  assert.deepEqual(Array.from(g.state.insets()), [8,24,12,16]);
  assert.equal(g.elements['glass-policy'].hidden, false);
  await g.elements['glass-policy'].click();
  await new Promise(resolve => setImmediate(resolve));
  assert(g.calls.includes('fullscreen'));
  assert(g.calls.includes('landscape-primary'));
  assert(g.calls.includes('wake'));
});
