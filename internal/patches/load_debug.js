(function () {
  var session = Math.random().toString(36).slice(2, 8);
  var originalFetch = window.fetch;

  function report(id, event, values) {
    var line = JSON.stringify(Object.assign({
      kind: "conversation-load", session: session, conversation: id,
      event: event, at: Math.round(performance.now()), ua: navigator.userAgent
    }, values));
    originalFetch("/__agy/api/debug/log", {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "text/plain" }, body: line + "\n"
    }).catch(function () {});
  }

  window.fetch = function (input, init) {
    var url = typeof input === "string" ? input : input.url;
    if (url.indexOf("/StreamAgentStateUpdates") === -1) return originalFetch.apply(this, arguments);
    var id = "unknown";
    try { id = JSON.parse(new TextDecoder().decode(init.body.subarray(5))).conversationId; } catch (_) {}
    var started = performance.now();
    report(id, "stream-request", {});
    return originalFetch.apply(this, arguments).then(function (response) {
      report(id, "stream-headers", { elapsed: Math.round(performance.now() - started), status: response.status });
      return response;
    }, function (error) {
      report(id, "stream-error", { elapsed: Math.round(performance.now() - started), error: error.name });
      throw error;
    });
  };

  var wrap = globalThis.__agyHistory.wrapProvider;
  globalThis.__agyHistory.wrapProvider = function (provider) {
    var id = provider.getState().conversationId;
    var started = performance.now();
    var stateAt;
    var finished = false;
    var observer;
    var frame;
    var subscription;
    var waiting;

    function cleanup() {
      finished = true;
      clearTimeout(waiting);
      if (subscription) subscription.dispose();
      if (observer) observer.disconnect();
      if (frame !== undefined) cancelAnimationFrame(frame);
    }
    function checkPaint() {
      if (finished || frame !== undefined || location.pathname.indexOf(id) === -1) return;
      if (!document.querySelector('[data-testid="conversation-view"] [role="article"]')) return;
      frame = requestAnimationFrame(function () {
        frame = requestAnimationFrame(function () {
          report(id, "messages-painted", {
            elapsed: Math.round(performance.now() - started),
            render: Math.round(performance.now() - stateAt),
            bundles: performance.getEntriesByType("resource").filter(function (entry) {
              return /\/main\.js/.test(entry.name);
            }).map(function (entry) {
              return { start: Math.round(entry.startTime), duration: Math.round(entry.duration), bytes: entry.transferSize };
            })
          });
          cleanup();
        });
      });
    }
    function checkState() {
      if (finished || stateAt !== undefined || !provider.getState().trajectorySlice) return;
      stateAt = performance.now();
      report(id, "first-state", { elapsed: Math.round(stateAt - started) });
      subscription.dispose();
      observer = new MutationObserver(checkPaint);
      observer.observe(document.documentElement, { childList: true, subtree: true });
      checkPaint();
    }
    subscription = provider.onDidChange(checkState);
    waiting = setTimeout(function () {
      report(id, "still-waiting", { elapsed: Math.round(performance.now() - started), receivedState: stateAt !== undefined });
    }, 5000);
    report(id, "provider-created", {});
    checkState();
    var result = wrap(provider);
    var dispose = result.dispose;
    result.dispose = function () { cleanup(); dispose(); };
    return result;
  };
})();
