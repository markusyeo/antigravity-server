(function () {
  var statuses = new Map();

  function bounds(slice, total) {
    var start = slice.startIndex;
    if (start < 0) start += total;
    start = Math.max(0, Math.min(total, start));
    var end = slice.endIndexExclusive;
    if (end === undefined) end = total;
    else if (end < 0) end += total;
    return { start: start, end: Math.max(start, Math.min(total, end)) };
  }

  function covers(state, requested) {
    var trajectory = state.trajectorySlice;
    if (!trajectory) return false;
    var loaded = bounds(trajectory.stepsSlice, trajectory.totalStepsLength);
    var target = bounds(requested, trajectory.totalStepsLength);
    return loaded.start === target.start && loaded.end === target.end;
  }

  function publish(id, status) {
    if (status === "idle") statuses.delete(id);
    else statuses.set(id, status);
    if (typeof document !== "undefined") renderStatus();
  }

  function wrapProvider(provider) {
    var active = null;
    var disposed = false;
    var id = provider.getState().conversationId;

    function requestPageUpdate(requested) {
      if (disposed) return Promise.reject(new Error("Conversation closed"));
      if (active) {
        if (JSON.stringify(active.requested) === JSON.stringify(requested)) return active.promise;
        return active.promise.then(function () { return requestPageUpdate(requested); });
      }
      var state = provider.getState();
      var trajectory = state.trajectorySlice;
      var older = trajectory && bounds(requested, trajectory.totalStepsLength).start < bounds(trajectory.stepsSlice, trajectory.totalStepsLength).start;
      var resolve, reject;
      var promise = new Promise(function (yes, no) { resolve = yes; reject = no; });
      var job = { promise: promise, requested: requested, cancel: null };
      active = job;
      var acknowledged = false;
      var response;
      var finished = false;
      var subscription;
      var timeout;

      function finish(error) {
        if (finished) return;
        finished = true;
        clearTimeout(timeout);
        if (subscription) subscription.dispose();
        active = null;
        if (older) publish(id, error && !disposed ? "error" : "idle");
        if (error) reject(error);
        else resolve(response);
      }
      function check() {
        if (acknowledged && covers(provider.getState(), requested)) finish();
      }
      job.cancel = function () { finish(new Error("Conversation closed")); };
      subscription = provider.onDidChange(check);
      timeout = setTimeout(function () { finish(new Error("History page did not arrive")); }, 15000);
      if (older) publish(id, "loading");
      Promise.resolve().then(function () { if (disposed) throw new Error("Conversation closed"); return provider.requestPageUpdate(requested); }).then(function (result) {
        response = result;
        acknowledged = true;
        check();
      }, finish);
      return promise;
    }

    return Object.assign({}, provider, {
      requestPageUpdate: requestPageUpdate,
      dispose: function () {
        disposed = true;
        if (active) active.cancel();
        publish(id, "idle");
        provider.dispose();
      }
    });
  }

  globalThis.__agyHistory = { wrapProvider: wrapProvider };
  if (typeof document === "undefined") return;

  var statusNode;
  var statusLabel;
  function renderStatus() {
    var status;
    statuses.forEach(function (value, id) {
      if (location.href.indexOf(id) !== -1) status = value;
    });
    var viewport = document.querySelector('[data-testid="conversation-view"] [data-testid="autoscroll-viewport"]');
    if (!viewport) {
      if (statusNode) statusNode.remove();
      return;
    }
    if (!statusNode || !statusNode.isConnected || statusNode.parentNode !== viewport) {
      if (statusNode) statusNode.remove();
      statusNode = document.createElement("div");
      statusNode.className = "agy-history-status";
      statusNode.setAttribute("role", "status");
      statusNode.setAttribute("aria-live", "polite");
      statusNode.setAttribute("aria-atomic", "true");
      statusLabel = document.createElement("span");
      statusNode.appendChild(statusLabel);
      viewport.prepend(statusNode);
    }
    var text = status === "loading" ? "Loading older messages…" : status === "error" ? "Couldn't load older messages. Scroll up to retry." : "";
    if (statusLabel.textContent !== text) statusLabel.textContent = text;
    statusNode.hidden = !status;
    statusNode.dataset.loading = status === "loading" ? "true" : "false";
  }
  new MutationObserver(renderStatus).observe(document.documentElement, { childList: true, subtree: true });
  window.addEventListener("popstate", renderStatus);
})();
