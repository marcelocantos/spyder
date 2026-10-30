// Browser entry-path oracle: evaluate the shipped inline script, not a copy.
// No wasm or running daemon required; physical phone playback is a separate gate.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const html = fs.readFileSync(__dirname + '/index.html', 'utf8');
const script = html.match(/<script>\s*([\s\S]*?)<\/script>/)?.[1];
assert.ok(script, 'browser entry script absent');
function launch(query, agent, hostname='192.168.1.23', port='3030') {
  const elements = {
    status: {textContent: '', style: {}},
    canvas: {width: 0, height: 0},
  };
  const ctx = {
    URLSearchParams, navigator: {userAgent: agent},
    location: {search: query, hostname, port},
    document: {title: '', getElementById: id => elements[id]},
    innerWidth: 300, innerHeight: 400, devicePixelRatio: 2,
    sessionStorage: {getItem: () => null, setItem: () => {}},
    console, setTimeout: () => {}, Date,
  };
  vm.createContext(ctx);
  vm.runInContext(script, ctx);
  return {args: Array.from(vm.runInContext('Module.arguments', ctx)), elements, title: ctx.document.title};
}
const phone = launch('?host=192.168.1.23&port=3030&name=tiltbuggy', 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0)');
assert.deepEqual(phone.args, ['--host','192.168.1.23','--port','3030','--name','tiltbuggy','--device-class','phone']);
assert.equal(phone.elements.canvas.width, 600);
assert.equal(phone.title, 'spyder player — tiltbuggy');
assert.deepEqual(launch('?name=maze&class=tablet', 'Mozilla/5.0 (Macintosh)').args,
  ['--host','192.168.1.23','--port','3030','--name','maze','--device-class','tablet']);
assert.deepEqual(launch('?name=maze', 'Mozilla/5.0 (iPad)').args.slice(-2), ['--device-class','tablet']);
console.log('browser player query → wasm argv and phone class: OK');
