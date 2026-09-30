const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const source = fs.readFileSync(path.join(__dirname, 'load_debug.js'), 'utf8');

function fixture() {
  let state = { conversationId: 'thread' };
  let visible = false;
  let disconnected = false;
  let disposed = false;
  let mutation;
  let next = 0;
  const calls = [], listeners = new Set(), frames = new Map(), timers = new Map();
  const response = { status: 200 };
  const originalFetch = async (url, init) => { calls.push({ url, init }); return response; };
  const provider = {
    getState: () => state,
    onDidChange(callback) { listeners.add(callback); return { dispose() { listeners.delete(callback); } }; },
    dispose() { disposed = true; }
  };
  const context = vm.createContext({
    window: { fetch: originalFetch }, TextDecoder,
    navigator: { userAgent: 'test browser' }, location: { pathname: '/c/thread' },
    performance: { now: () => 100, getEntriesByType: () => [] },
    document: { documentElement: {}, querySelector: () => visible ? {} : null },
    MutationObserver: class {
      constructor(callback) { mutation = callback; }
      observe() {}
      disconnect() { disconnected = true; }
    },
    requestAnimationFrame(callback) { const id = ++next; frames.set(id, callback); return id; },
    cancelAnimationFrame(id) { frames.delete(id); },
    setTimeout(callback) { const id = ++next; timers.set(id, callback); return id; },
    clearTimeout(id) { timers.delete(id); },
    __agyHistory: { wrapProvider: p => p }
  });
  vm.runInContext(source, context);
  return {
    context, provider, calls, response, frames, timers, listeners,
    update() { state = { ...state, trajectorySlice: {} }; listeners.forEach(callback => callback()); },
    show() { visible = true; mutation(); },
    frame() { const pending = [...frames.values()]; frames.clear(); pending.forEach(callback => callback()); },
    events() { return calls.filter(c => c.url.includes('/debug/log')).map(c => JSON.parse(c.init.body).event); },
    get disconnected() { return disconnected; }, get disposed() { return disposed; }
  };
}

test('debug waits for state and a visible message, then releases observers', () => {
  const f = fixture();
  f.context.__agyHistory.wrapProvider(f.provider);
  assert.deepEqual(f.events(), ['provider-created']);
  f.update();
  assert.deepEqual(f.events(), ['provider-created', 'first-state']);
  assert.equal(f.frames.size, 0);
  f.show();
  f.frame();
  assert.equal(f.events().includes('messages-painted'), false);
  f.frame();
  assert.equal(f.events().includes('messages-painted'), true);
  assert.equal(f.disconnected, true);
  assert.equal(f.listeners.size, 0);
  assert.equal(f.timers.size, 0);
});

test('debug passes through the stream response and logs only timing fields', async () => {
  const f = fixture();
  const json = new TextEncoder().encode(JSON.stringify({ conversationId: 'thread', secret: 'private content' }));
  const body = new Uint8Array(5 + json.length);
  body.set(json, 5);
  const response = await f.context.window.fetch('/StreamAgentStateUpdates', { body });
  assert.equal(response, f.response);
  assert.deepEqual(f.events(), ['stream-request', 'stream-headers']);
  assert.equal(f.calls.filter(c => c.url.includes('/debug/log')).some(c => c.init.body.includes('private content')), false);
});

test('closing an unloaded conversation cancels its diagnostic subscription', () => {
  const f = fixture();
  const provider = f.context.__agyHistory.wrapProvider(f.provider);
  provider.dispose();
  assert.equal(f.listeners.size, 0);
  assert.equal(f.timers.size, 0);
  assert.equal(f.disposed, true);
});
