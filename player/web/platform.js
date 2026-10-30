// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
// Browser-only glass policy. No capability is asserted until a finite motion
// sample arrives after an explicit user gesture (required by iOS Safari).
(function (g) {
  const state = {motion: null, revision: 0, error: '', wake: null, config: null};
  const canvas = document.getElementById('canvas');
  const button = document.createElement('button');
  button.id = 'motion-permission';
  button.textContent = 'Enable tilt controls';
  button.style.cssText = 'position:fixed;z-index:5;left:env(safe-area-inset-left, 0px);bottom:calc(20px + env(safe-area-inset-bottom, 0px));font:16px system-ui;padding:12px';
  button.hidden = true;
  document.body.append(button);
  const probe = document.createElement('div');
  probe.style.cssText = 'position:fixed;pointer-events:none;visibility:hidden;padding:env(safe-area-inset-top, 0px) env(safe-area-inset-right, 0px) env(safe-area-inset-bottom, 0px) env(safe-area-inset-left, 0px)';
  document.body.append(probe);
  const notice = document.createElement('div');
  notice.id = 'glass-notice';
  notice.setAttribute('role', 'status');
  notice.style.cssText = 'position:fixed;z-index:5;top:calc(12px + env(safe-area-inset-top, 0px));left:env(safe-area-inset-left, 0px);right:env(safe-area-inset-right, 0px);color:white;background:#500b;padding:8px;font:14px system-ui';
  notice.hidden = true;
  document.body.append(notice);
  const report = (message) => {
    state.error = notice.textContent = message;
    notice.hidden = false;
    console.warn('spyder glass: ' + message);
  };
  const supported = () => g.isSecureContext && typeof g.DeviceMotionEvent !== 'undefined';
  function motion(e) {
    const a = e.accelerationIncludingGravity;
    if (!a || ![a.x, a.y, a.z].every(Number.isFinite)) return;
    // SDL sensors use m/s²; leave the hardware-frame rotation to PlayerRender.
    state.motion = [a.x, a.y, a.z];
    state.revision++;
    button.hidden = true;
  }
  async function enableMotion() {
    if (!supported()) { report('tilt requires HTTPS and DeviceMotion'); return; }
    // Call requestPermission synchronously in the click handler, before any await.
    try {
      const request = g.DeviceMotionEvent.requestPermission;
      const permission = request ? await request.call(g.DeviceMotionEvent) : 'granted';
      if (permission !== 'granted') { report('tilt permission denied'); return; }
      g.addEventListener('devicemotion', motion);
      button.textContent = 'Waiting for motion…';
    } catch (e) { report('tilt permission failed: ' + e); }
  }
  button.addEventListener('click', enableMotion);
  async function wake() {
    if (!state.config?.noSaver || document.hidden || state.wake) return;
    if (!g.isSecureContext || !navigator.wakeLock?.request) { report('screen wake lock unsupported'); return; }
    try {
      state.wake = await navigator.wakeLock.request('screen');
      state.wake.addEventListener('release', () => { state.wake = null; });
    } catch (e) { report('screen wake lock denied: ' + e); }
  }
  document.addEventListener('visibilitychange', wake);
  async function immersive() {
    if (!state.config?.immersive) return;
    if (!canvas.requestFullscreen) { report('fullscreen unsupported (iOS Safari may not support canvas fullscreen)'); return; }
    try { await canvas.requestFullscreen(); } catch (e) { report('fullscreen denied: ' + e); }
  }
  async function orient() {
    const o = state.config?.orientation;
    if (!o) return;
    const names = {1:'landscape-primary', 2:'landscape-secondary', 3:'portrait-primary', 4:'portrait-secondary', 254:'landscape'};
    if (!screen.orientation?.lock) { report('orientation lock unsupported'); return; }
    try { await screen.orientation.lock(names[o]); } catch (e) { report('orientation lock denied: ' + e); }
  }
  // Browser policy APIs often require a gesture. Keep the button for policy
  // even when motion isn't requested, so no request is silently lost.
  const policyButton = document.createElement('button');
  policyButton.id = 'glass-policy';
  policyButton.textContent = 'Apply display policy';
  policyButton.style.cssText = 'position:fixed;z-index:5;right:env(safe-area-inset-right, 0px);bottom:calc(20px + env(safe-area-inset-bottom, 0px));font:16px system-ui;padding:12px';
  policyButton.hidden = true;
  document.body.append(policyButton);
  policyButton.addEventListener('click', () => {
    notice.hidden = true; state.error = '';
    immersive(); orient(); wake();
    policyButton.hidden = true;
  });
  function insets() {
    const s = getComputedStyle(probe);
    return [s.paddingLeft, s.paddingTop, s.paddingRight, s.paddingBottom].map(x => Math.max(0, parseFloat(x) || 0) * (g.devicePixelRatio || 1));
  }
  g.spyderPlatform = {
    state, insets,
    configure(sensors, orientation, flags) {
      state.config = {sensors, orientation, immersive: !!(flags & 1), noSaver: !!(flags & 2)};
      button.hidden = !(sensors & 1) || !supported() || !!state.motion;
      if ((sensors & 1) && !supported()) report('tilt requires HTTPS and DeviceMotion');
      policyButton.hidden = !state.config.immersive && !orientation && !state.config.noSaver;
      wake();
    },
    takeMotion(last) { return state.revision > last ? {revision:state.revision, values:state.motion} : null; },
  };
})(globalThis);
