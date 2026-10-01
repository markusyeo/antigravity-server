const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const source = fs.readFileSync(path.join(__dirname, 'load_debug.js'), 'utf8');

function fixture(streamResponse) {
  let state = { conversationId: 'thread' };
  let visible = false;
  let disconnected = false;
  let disposed = false;
  let mutation;
  let next = 0;
  const calls = [], listeners = new Set(), frames = new Map(), timers = new Map();
  const response = streamResponse || { status: 200 };
  const originalFetch = async (url, init) => { calls.push({ url, init }); return response; };
  const provider = {
    getState: () => state,
    onDidChange(callback) { listeners.add(callback); return { dispose() { listeners.delete(callback); } }; },
    dispose() { disposed = true; }
  };
  const context = vm.createContext({
    window: { fetch: originalFetch }, TextDecoder, URL,
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
    events() { return calls.filter(c => c.url.includes('/debug/log')).map(c => JSON.parse(c.init.body).event).filter(event => event !== 'page-start'); },
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

test('stream diagnostics count a fragmented Connect frame without consuming ahead', async () => {
  const payload = new TextEncoder().encode('{"update":{}}');
  const frame = new Uint8Array(5 + payload.length);
  new DataView(frame.buffer).setUint32(1, payload.length);
  frame.set(payload, 5);
  let pulls = 0;
  const body = new ReadableStream({
    start(controller) {
      controller.enqueue(frame.subarray(0, 2));
      controller.enqueue(frame.subarray(2, 7));
      controller.enqueue(frame.subarray(7));
      controller.close();
    }
  });
  const originalGetReader = body.getReader;
  body.getReader = function () {
    const reader = originalGetReader.apply(this, arguments);
    const read = reader.read;
    reader.read = function () { pulls++; return read.apply(this, arguments); };
    return reader;
  };
  const response = new Response(body, { headers: { 'X-Request-ID': 'request-1', 'Content-Encoding': 'gzip' } });
  const f = fixture(response);
  assert.equal(await f.context.window.fetch('/StreamAgentStateUpdates'), response);
  assert.equal(pulls, 0);
  const reader = response.body.getReader();
  const chunks = [];
  for (;;) {
    const chunk = await reader.read();
    if (chunk.done) break;
    chunks.push(chunk.value);
  }
  assert.deepEqual(Buffer.concat(chunks), Buffer.from(frame));
  const reports = f.calls.filter(c => c.url.includes('/debug/log')).map(c => JSON.parse(c.init.body));
  assert.equal(reports.filter(r => r.event === 'stream-first-byte').length, 1);
  const received = reports.filter(r => r.event === 'stream-first-frame');
  assert.equal(received.length, 1);
  assert.equal(received[0].bytes, frame.length);
  assert.equal(received[0].requestId, 'request-1');
  assert.equal(pulls, 4);
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

test('aborting a stream is recorded as cancellation while preserving its rejection', async () => {
  const controller = new AbortController();
  const failure = new TypeError('cancelled reader');
  const response = new Response(new ReadableStream({ start(stream) { stream.error(failure); } }));
  const f = fixture(response);
  await f.context.window.fetch('/StreamAgentStateUpdates', { signal: controller.signal });
  controller.abort();
  await assert.rejects(response.body.getReader().read(), error => error === failure);
  assert.equal(f.events().includes('stream-cancelled'), true);
  assert.equal(f.events().includes('stream-read-error'), false);
});

test('cached resources and unsupported browser metrics are reported as unavailable', () => {
  const f = fixture();
  f.context.performance.getEntriesByType = type => type === 'resource' ? [{
    name: 'https://example.test/prism_bundle.js', startTime: 442, duration: 0,
    requestStart: 442, responseStart: 0, transferSize: 0, encodedBodySize: 0, decodedBodySize: 0
  }] : [];
  f.context.__agyHistory.wrapProvider(f.provider);
  f.update();
  f.show();
  f.frame();
  f.frame();
  const painted = f.calls.filter(c => c.url.includes('/debug/log')).map(c => JSON.parse(c.init.body)).find(row => row.event === 'messages-painted');
  assert.equal(painted.bundles[0].ttfb, null);
  assert.equal(painted.longTasks, null);
  assert.equal(painted.blocked, null);
});

test('closing an unloaded conversation cancels its diagnostic subscription', () => {
  const f = fixture();
  const provider = f.context.__agyHistory.wrapProvider(f.provider);
  provider.dispose();
  assert.equal(f.listeners.size, 0);
  assert.equal(f.timers.size, 0);
  assert.equal(f.disposed, true);
});
