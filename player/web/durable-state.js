// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
// Browser SP2T store. Loaded with --pre-js so it shares Emscripten's FS scope.
// The mount is populated BEFORE the player opens its sqlite database or sends
// a seed. One syncfs writer at a time; pending snapshots are latest-wins.
var SpyderDurableState = (function () {
  var root = '/spyder-state';
  var ready = false;
  var pending = null;
  var writing = false;
  var waiters = [];
  var lastError = null;

  function sync(FS, populate) {
    return new Promise(function (resolve, reject) {
      FS.syncfs(populate, function (err) { err ? reject(err) : resolve(); });
    });
  }

  function settle() {
    if (writing || pending) return;
    var w = waiters.splice(0);
    w.forEach(function (entry) {
      lastError ? entry.reject(lastError) : entry.resolve();
    });
  }

  function drain(FS) {
    if (writing || !pending) return;
    writing = true;
    var next = pending;
    pending = null;
    try {
      FS.writeFile(next.path, next.bytes);
      sync(FS, false).then(function () {
        lastError = null;
        writing = false;
        drain(FS);
        settle();
      }, function (err) {
        lastError = err;
        console.error('SP2T: browser sync failed:', err);
        writing = false;
        drain(FS);
        settle();
      });
    } catch (err) {
      lastError = err;
      console.error('SP2T: browser write failed:', err);
      writing = false;
      drain(FS);
      settle();
    }
  }

  return {
    root: root,
    init: async function (FS, IDBFS) {
      try {
        FS.mkdir(root);
        FS.mount(IDBFS, {}, root);
        await sync(FS, true);
        try { FS.mkdir(root + '/games'); }
        catch (err) { if (!FS.analyzePath(root + '/games').exists) throw err; }
        ready = true;
        return true;
      } catch (err) {
        console.error('SP2T: browser persistent storage unavailable:', err);
        return false; // fail soft; never seed from an unpopulated mount
      }
    },
    write: function (FS, path, bytes) {
      if (!ready || !path.startsWith(root + '/games/')) return false;
      pending = { path: path, bytes: bytes };
      drain(FS);
      return true;
    },
    flush: function () {
      if (!writing && !pending) {
        return lastError ? Promise.reject(lastError) : Promise.resolve();
      }
      return new Promise(function (resolve, reject) {
        waiters.push({ resolve: resolve, reject: reject });
      });
    }
  };
})();
