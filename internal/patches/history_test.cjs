const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const source = fs.readFileSync(path.join(__dirname, 'history.js'), 'utf8');
const tick = () => new Promise(resolve => setImmediate(resolve));

function fixture() {
  const timers = new Map();
  let nextTimer = 0;
  const context = vm.createContext({
    setTimeout(callback) { const id = ++nextTimer; timers.set(id, callback); return id; },
    clearTimeout(id) { timers.delete(id); }
  });
  vm.runInContext(source, context);
  const listeners = new Set();
  const requests = [];
  let state = { conversationId: 'thread', trajectorySlice: { totalStepsLength: 500, stepsSlice: { startIndex: -15 } } };
  let disposals = 0;
  const provider = context.__agyHistory.wrapProvider({
    getState: () => state,
    onDidChange(callback) { listeners.add(callback); return { dispose() { listeners.delete(callback); } }; },
    requestPageUpdate(bounds) { return new Promise((resolve, reject) => requests.push({ bounds, resolve, reject })); },
    dispose() { disposals++; }
  });
  return { provider, requests, listeners, timers,
    get disposals() { return disposals; },
    update(slice, total = 500) {
      state = slice ? { conversationId: 'thread', trajectorySlice: { totalStepsLength: total, stepsSlice: slice } } : { conversationId: 'thread' };
      listeners.forEach(callback => callback(state));
    }
  };
}

test('RPC acknowledgment waits for the matching streamed page', async () => {
  const f = fixture();
  let finished = false;
  const pending = f.provider.requestPageUpdate({ startIndex: 385 }).then(value => { finished = true; return value; });
  await tick();
  const response = { accepted: true };
  f.requests[0].resolve(response);
  await tick();
  assert.equal(finished, false);
  f.update({ startIndex: 400 });
  await tick();
  assert.equal(finished, false);
  f.update({ startIndex: 385 });
  assert.equal(await pending, response);
  assert.equal(f.listeners.size, 0);
  assert.equal(f.timers.size, 0);
});

test('stream arrival before acknowledgment also waits', async () => {
  const f = fixture();
  let finished = false;
  const pending = f.provider.requestPageUpdate({ startIndex: -115 }).then(() => { finished = true; });
  await tick();
  f.update({ startIndex: 385 });
  await tick();
  assert.equal(finished, false);
  f.requests[0].resolve({});
  await pending;
});

test('identical requests share one promise; different pages serialize', async () => {
  const f = fixture();
  const first = f.provider.requestPageUpdate({ startIndex: 385 });
  assert.equal(f.provider.requestPageUpdate({ startIndex: 385 }), first);
  const next = f.provider.requestPageUpdate({ startIndex: 285 });
  await tick();
  assert.equal(f.requests.length, 1);
  f.requests[0].resolve({});
  f.update({ startIndex: 385 });
  await first;
  await tick();
  assert.equal(f.requests.length, 2);
  f.requests[1].resolve({});
  f.update({ startIndex: 285 });
  await next;
});

test('contraction and reconnect cannot complete on an old wider page', async () => {
  const f = fixture();
  f.update({ startIndex: 200 });
  let finished = false;
  const pending = f.provider.requestPageUpdate({ startIndex: 300 }).then(() => { finished = true; });
  await tick();
  f.requests[0].resolve({});
  await tick();
  assert.equal(finished, false);
  f.update(null);
  await tick();
  assert.equal(finished, false);
  f.update({ startIndex: 300 });
  await pending;
});

test('errors, timeout, and disposal release the subscription and timer', async () => {
  for (const reason of ['network', 'timeout', 'dispose']) {
    const f = fixture();
    const pending = f.provider.requestPageUpdate({ startIndex: 385 });
    const rejected = assert.rejects(pending);
    await tick();
    if (reason === 'network') f.requests[0].reject(new Error('Unavailable'));
    if (reason === 'timeout') [...f.timers.values()].forEach(callback => callback());
    if (reason === 'dispose') f.provider.dispose();
    await rejected;
    assert.equal(f.listeners.size, 0);
    assert.equal(f.timers.size, 0);
    if (reason === 'dispose') assert.equal(f.disposals, 1);
  }
});
