(function () {
  var shell;
  var status;
  var label;
  var retry;
  var path = location.pathname;
  var started = performance.now();
  var scheduled = false;
  var draftBlocked = false;

  function clearStatus() {
    if (status) {
      if (status.parentNode) status.parentNode.classList.remove("agy-loading-group");
      status.remove();
    }
    status = null;
    draftBlocked = false;
  }

  function update() {
    scheduled = false;
    var root = document.getElementById("root");
    if (root && !root.firstChild && !shell) {
      shell = document.createElement("div");
      shell.className = "agy-loading-shell";
      shell.setAttribute("role", "status");
      shell.textContent = "Opening Antigravity…";
      root.appendChild(shell);
    }
    if (path !== location.pathname) {
      path = location.pathname;
      started = performance.now();
      clearStatus();
    }
    var view = document.querySelector('[data-testid="conversation-view"]');
    var messages = view && view.querySelector('[role="article"]');
    var spinner = view && view.querySelector('.animate-spin, [name="progress_activity"]');
    if (path.indexOf("/c/") !== 0 || !view || messages || !spinner) {
      clearStatus();
      return;
    }
    var elapsed = performance.now() - started;
    if (elapsed < 1500) return;
    if (!status || !status.isConnected) {
      status = document.createElement("div");
      status.className = "agy-conversation-loading";
      status.setAttribute("role", "status");
      status.setAttribute("aria-live", "polite");
      label = document.createElement("span");
      retry = document.createElement("button");
      retry.type = "button";
      retry.textContent = "Reload conversation";
      retry.onclick = function () {
        var composer = view.querySelector('[contenteditable="true"]');
        if (composer && composer.textContent.trim()) {
          draftBlocked = true;
          label.textContent = "Save your draft before reloading.";
          return;
        }
        location.reload();
      };
      status.appendChild(label);
      status.appendChild(retry);
      spinner.parentNode.classList.add("agy-loading-group");
      spinner.parentNode.appendChild(status);
    }
    var text = elapsed >= 10000 ? "This conversation is taking longer to open." : "Loading recent messages…";
    if (draftBlocked) {
      var composer = view.querySelector('[contenteditable="true"]');
      if (composer && composer.textContent.trim()) text = "Save your draft before reloading.";
      else draftBlocked = false;
    }
    if (label.textContent !== text) label.textContent = text;
    retry.hidden = elapsed < 15000;
  }
  function schedule() {
    if (scheduled) return;
    scheduled = true;
    requestAnimationFrame(update);
  }
  new MutationObserver(schedule).observe(document.documentElement, { childList: true, subtree: true });
  window.addEventListener("popstate", schedule);
  setInterval(schedule, 1000);
  // The root may arrive before the application script has downloaded.
  new MutationObserver(function (_, observer) {
    if (document.getElementById("root")) { update(); observer.disconnect(); }
  }).observe(document.documentElement, { childList: true, subtree: true });
})();
