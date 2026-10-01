(function () {
  var session = Math.random().toString(36).slice(2, 8);
  var originalFetch = window.fetch;
  var clicks = new Map();
  var longTasks = { count: 0, duration: 0, max: 0 };
  var longTaskSupported = typeof PerformanceObserver !== "undefined" &&
    (PerformanceObserver.supportedEntryTypes || []).indexOf("longtask") !== -1;

  function report(id, event, values) {
    var line = JSON.stringify(Object.assign({
      kind: "conversation-load", session: session, conversation: id,
      event: event, at: Math.round(performance.now()), time: new Date().toISOString(),
      visibility: document.visibilityState, ua: navigator.userAgent
    }, values));
    originalFetch("/__agy/api/debug/log", {
      method: "POST", credentials: "same-origin", keepalive: true,
      headers: { "Content-Type": "text/plain" }, body: line + "\n"
    }).catch(function () {});
  }

  function resources() {
    return performance.getEntriesByType("resource").filter(function (entry) {
      return /\/(main|prism)_bundle?\.js|\/main\.js/.test(entry.name);
    }).map(function (entry) {
      return { name: new URL(entry.name).pathname, start: Math.round(entry.startTime),
        duration: Math.round(entry.duration),
        ttfb: entry.responseStart > 0 && entry.responseStart >= entry.requestStart ? Math.round(entry.responseStart - entry.requestStart) : null,
        bytes: entry.transferSize, encoded: entry.encodedBodySize, decoded: entry.decodedBodySize,
        protocol: entry.nextHopProtocol };
    });
  }
  if (longTaskSupported) {
    try {
      new PerformanceObserver(function (list) {
        list.getEntries().forEach(function (entry) {
          longTasks.count++;
          longTasks.duration += entry.duration;
          longTasks.max = Math.max(longTasks.max, entry.duration);
        });
      }).observe({ type: "longtask", buffered: true });
    } catch (_) { longTaskSupported = false; }
  }
  if (document.addEventListener) {
    document.addEventListener("click", function (event) {
      var link = event.target.closest && event.target.closest('a[href*="/c/"]');
      if (!link) return;
      var match = new URL(link.href, location.href).pathname.match(/^\/c\/([^/]+)/);
      if (!match) return;
      clicks.clear();
      clicks.set(match[1], performance.now());
      report(match[1], "thread-click", {});
    }, true);
  }
  report(null, "page-start", {});
  if (window.addEventListener) window.addEventListener("load", function () {
    report(null, "page-ready", { bundles: resources(),
      paints: performance.getEntriesByType("paint").map(function (entry) { return { name: entry.name, at: Math.round(entry.startTime) }; }),
      blocked: longTaskSupported ? Math.round(longTasks.duration) : null });
  });

  window.fetch = function (input, init) {
    var url = typeof input === "string" ? input : input.url || String(input);
    if (url.indexOf("/StreamAgentStateUpdates") === -1) return originalFetch.apply(this, arguments);
    var id = "unknown";
    try { id = JSON.parse(new TextDecoder().decode(init.body.subarray(5))).conversationId; } catch (_) {}
    var started = performance.now();
    var signal = init && init.signal || typeof input === "object" && input.signal;
    report(id, "stream-request", {});
    return originalFetch.apply(this, arguments).then(function (response) {
      var requestId = response.headers && response.headers.get("X-Request-ID");
      report(id, "stream-headers", { elapsed: Math.round(performance.now() - started), status: response.status,
        requestId: requestId, encoding: response.headers && response.headers.get("Content-Encoding"),
        connectionId: response.headers && response.headers.get("X-Connection-ID"),
        serverTiming: response.headers && response.headers.get("Server-Timing") });
      if (response.body) {
        var getReader = response.body.getReader;
        response.body.getReader = function () {
          var reader = getReader.apply(this, arguments);
          var read = reader.read;
          var bytes = 0, header = [], expected, received = false;
          reader.read = function () {
            return read.apply(this, arguments).then(function (chunk) {
              if (!chunk.done && !received) {
                if (!bytes) report(id, "stream-first-byte", { elapsed: Math.round(performance.now() - started), requestId: requestId });
                bytes += chunk.value.byteLength;
                for (var i = 0; header.length < 5 && i < chunk.value.length; i++) header.push(chunk.value[i]);
                if (header.length === 5 && expected === undefined) {
                  expected = 5 + ((header[1] * 16777216) + (header[2] << 16) + (header[3] << 8) + header[4]);
                }
                if (expected !== undefined && bytes >= expected) {
                  received = true;
                  report(id, "stream-first-frame", { elapsed: Math.round(performance.now() - started), bytes: expected, requestId: requestId });
                }
              }
              if (chunk.done && !received) report(id, "stream-ended-before-state", { elapsed: Math.round(performance.now() - started), bytes: bytes, requestId: requestId });
              return chunk;
            }, function (error) {
              report(id, signal && signal.aborted ? "stream-cancelled" : "stream-read-error", {
                elapsed: Math.round(performance.now() - started), error: error.name, requestId: requestId });
              throw error;
            });
          };
          return reader;
        };
      }
      return response;
    }, function (error) {
      report(id, signal && signal.aborted ? "stream-cancelled" : "stream-error", { elapsed: Math.round(performance.now() - started), error: error.name });
      throw error;
    });
  };

  var wrap = globalThis.__agyHistory.wrapProvider;
  globalThis.__agyHistory.wrapProvider = function (provider) {
    var id = provider.getState().conversationId;
    var created = performance.now();
    var started = clicks.has(id) ? clicks.get(id) : created;
    clicks.delete(id);
    var stateAt;
    var finished = false;
    var observer;
    var frame;
    var subscription;
    var waiting;
    var taskStart = { count: longTasks.count, duration: longTasks.duration };

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
      report(id, "messages-mounted", { elapsed: Math.round(performance.now() - started), render: Math.round(performance.now() - stateAt) });
      frame = requestAnimationFrame(function () {
        frame = requestAnimationFrame(function () {
          if (location.pathname.indexOf(id) === -1 || !document.querySelector('[data-testid="conversation-view"] [role="article"]')) {
            frame = undefined;
            return;
          }
          report(id, "messages-painted", {
            elapsed: Math.round(performance.now() - started),
            render: Math.round(performance.now() - stateAt), bundles: resources(),
            longTasks: longTaskSupported ? longTasks.count - taskStart.count : null,
            blocked: longTaskSupported ? Math.round(longTasks.duration - taskStart.duration) : null,
            paints: performance.getEntriesByType("paint").map(function (entry) { return { name: entry.name, at: Math.round(entry.startTime) }; })
          });
          cleanup();
        });
      });
    }
    function checkState() {
      if (finished || stateAt !== undefined || !provider.getState().trajectorySlice) return;
      stateAt = performance.now();
      report(id, "first-state", { elapsed: Math.round(stateAt - started) });
      if (subscription) subscription.dispose();
      observer = new MutationObserver(checkPaint);
      observer.observe(document.documentElement, { childList: true, subtree: true });
      checkPaint();
    }
    subscription = provider.onDidChange(checkState);
    waiting = setTimeout(function () {
      report(id, "still-waiting", { elapsed: Math.round(performance.now() - started), receivedState: stateAt !== undefined, bundles: resources() });
    }, 5000);
    report(id, "provider-created", { elapsed: Math.round(created - started) });
    checkState();
    var result = wrap(provider);
    var dispose = result.dispose;
    result.dispose = function () {
      if (!finished) report(id, "load-abandoned", { elapsed: Math.round(performance.now() - started), receivedState: stateAt !== undefined });
      cleanup(); dispose();
    };
    return result;
  };
})();
