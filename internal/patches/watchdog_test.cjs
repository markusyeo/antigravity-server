const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const registry = fs.readFileSync(path.join(__dirname, 'registry.go'), 'utf8');
const source = registry.match(/const connectionWatchdogScript = `<script[^>]*>([\s\S]*?)<\/script>`/)[1];

function fixture(loaded) {
  let now = 1000, reloads = 0, tick, streamController;
  const storage = new Map();
  const stream = new Response(new ReadableStream({ start(controller) { streamController = controller; } }), {
    headers: { 'Content-Type': 'application/connect+json' }
  });
  const view = {
    querySelector(selector) {
      if (selector.includes('[role="article"]')) return loaded ? {} : null;
      if (selector.includes('.animate-spin')) return {};
      return null;
    }
  };
  const context = vm.createContext({
    Date: { now: () => now }, console, TextDecoder,
    location: { pathname: '/c/thread', reload() { reloads++; } },
    document: {
      body: {}, readyState: 'complete', visibilityState: 'visible',
      querySelector: selector => selector.includes('conversation-view') ? view : null,
      querySelectorAll: () => [], addEventListener() {}
    },
    fetch: async url => url.includes('StreamAgentStateUpdates') ? stream : new Response('{"available":true}'),
    sessionStorage: { getItem: key => storage.get(key), setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) },
    MutationObserver: class { observe() {} },
    setInterval(callback) { tick = callback; },
    requestAnimationFrame() { return 1; }, addEventListener() {}
  });
  context.window = context;
  vm.runInContext(source, context);
  return {
    context, get reloads() { return reloads; },
    tick(time) { now = time; tick(); },
    chunk(time) { now = time; streamController.enqueue(new Uint8Array([1, 2, 3])); }
  };
}

async function settle() { for (let i = 0; i < 12; i++) await Promise.resolve(); }

test('current article markup prevents recovery reload while an agent spinner is visible', async () => {
  const f = fixture(true);
  f.tick(6000);
  f.tick(40000);
  await settle();
  assert.equal(f.reloads, 0);
});

test('incoming snapshot chunks prevent reload during a slow download', async () => {
  const f = fixture(false);
  const response = await f.context.fetch('/StreamAgentStateUpdates');
  const reader = response.body.getReader();
  f.tick(6000);
  f.chunk(39000);
  assert.equal((await reader.read()).value.byteLength, 3);
  f.tick(40000);
  await settle();
  assert.equal(f.reloads, 0);
});

test('an idle empty conversation still recovers after the watchdog deadline', async () => {
  const f = fixture(false);
  f.tick(6000);
  f.tick(40000);
  await settle();
  assert.equal(f.reloads, 1);
});

test('background health requests cannot postpone recovery of an empty conversation', async () => {
  const f = fixture(false);
  f.tick(6000);
  for (let time = 7000; time <= 40000; time += 1000) {
    await f.context.fetch('/__agy/api/signin/status');
    f.tick(time);
    await settle();
  }
  assert.equal(f.reloads, 1);
});

test('chunks from a previous thread cannot postpone recovery of the current conversation', async () => {
  const f = fixture(false);
  const response = await f.context.fetch('/StreamAgentStateUpdates');
  const reader = response.body.getReader();
  f.context.location.pathname = '/c/other';
  f.tick(6000);
  f.chunk(39000);
  await reader.read();
  f.tick(40000);
  await settle();
  assert.equal(f.reloads, 1);
});

test('a previous provider reconnecting on the current route cannot postpone recovery', async () => {
  const f = fixture(false);
  const payload = new TextEncoder().encode(JSON.stringify({ conversationId: 'other' }));
  const body = new Uint8Array(5 + payload.length);
  body.set(payload, 5);
  const response = await f.context.fetch('/StreamAgentStateUpdates', { body });
  const reader = response.body.getReader();
  f.tick(6000);
  f.chunk(39000);
  await reader.read();
  f.tick(40000);
  await settle();
  assert.equal(f.reloads, 1);
});

test('matching provider payload keeps a slow snapshot download protected', async () => {
  const f = fixture(false);
  const payload = new TextEncoder().encode(JSON.stringify({ conversationId: 'thread' }));
  const body = new Uint8Array(5 + payload.length);
  body.set(payload, 5);
  const response = await f.context.fetch('/StreamAgentStateUpdates', { body });
  const reader = response.body.getReader();
  f.tick(6000);
  f.chunk(39000);
  await reader.read();
  f.tick(40000);
  await settle();
  assert.equal(f.reloads, 0);
});
