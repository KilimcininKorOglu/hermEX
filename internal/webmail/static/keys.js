// Mail keyboard shortcuts (#34/#35, contract-map/07 §19). Only the bindings that
// do not collide with a browser default are wired: Ctrl+Alt+* action shortcuts,
// plus Ctrl+Enter / Ctrl+S in compose (universally expected in editors). The
// plain-Ctrl-letter bindings the reference uses for Reply (Ctrl+R = Reload),
// Forward (Ctrl+F = Find), and Edit-as-New (Ctrl+E) are intentionally left to the
// browser; those actions remain available from the toolbar.
(function () {
  "use strict";

  function isTyping(el) {
    if (!el) { return false; }
    var tag = el.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable;
  }

  // Click a control identified by data-key, preferring one inside the selected
  // (previewed) list row so the read/flag shortcuts act on the current message.
  function clickKey(name) {
    var el = document.querySelector("tr.selected [data-key='" + name + "']") ||
             document.querySelector("[data-key='" + name + "']");
    if (el) {
      el.click();
      return true;
    }
    return false;
  }

  document.addEventListener("keydown", function (e) {
    if (!(e.ctrlKey || e.metaKey)) { return; }

    // Compose: Send (Ctrl+Enter) and Save draft (Ctrl+S) fire even while typing.
    if ((e.code === "Enter" || e.code === "NumpadEnter") && !e.altKey) {
      if (clickKey("send")) { e.preventDefault(); }
      return;
    }
    if (e.code === "KeyS" && !e.altKey) {
      if (clickKey("savedraft")) { e.preventDefault(); }
      return;
    }

    // Remaining shortcuts use Ctrl+Alt+* (no browser collision); never while
    // typing in a field. e.code is layout-independent (Alt+letter may otherwise
    // produce a special character in e.key).
    if (!e.altKey || isTyping(e.target)) { return; }
    var handled = false;
    switch (e.code) {
      case "KeyX": // New mail — unless a compose form is already open.
        if (!document.querySelector("[data-key='send']")) {
          window.location.href = "/compose";
          handled = true;
        }
        break;
      case "KeyR": // Reply All
        handled = clickKey("replyall");
        break;
      case "KeyU": // Toggle read/unread (on the selected message)
        handled = clickKey("toggleread");
        break;
      case "KeyG": // Toggle follow-up flag (on the selected message)
        handled = clickKey("flagtoggle");
        break;
    }
    if (handled) { e.preventDefault(); }
  });
})();
