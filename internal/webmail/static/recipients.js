// recipients.js — GAL recipient autocomplete and "check names" for the compose
// form. It enhances the To/Cc/Bcc fields against the session-gated /resolve
// endpoint; with JavaScript off the fields are plain comma-separated inputs and
// the server still resolves recipients at send time, so this is purely additive.
(function () {
  "use strict";
  var form = document.getElementById("composeform");
  if (!form) { return; }
  var fields = ["to", "cc", "bcc"].map(function (n) { return form[n]; }).filter(Boolean);
  if (!fields.length) { return; }

  // A recipient field holds comma-separated addresses; completion acts on the
  // token the caret sits in (the text between the previous comma and the caret).
  function currentToken(input) {
    var v = input.value;
    var caret = input.selectionStart == null ? v.length : input.selectionStart;
    var start = v.lastIndexOf(",", caret - 1) + 1;
    return { start: start, end: caret, text: v.slice(start, caret).trim() };
  }
  function replaceToken(input, tok, addr) {
    var before = input.value.slice(0, tok.start).replace(/\s+$/, "");
    var after = input.value.slice(tok.end).replace(/^[\s,]+/, "");
    var lead = before === "" ? "" : before + ", ";
    var tail = after === "" ? ", " : ", " + after;
    input.value = lead + addr + tail;
    var pos = (lead + addr + ", ").length;
    input.setSelectionRange(pos, pos);
    input.focus();
  }

  // One suggestion dropdown at a time, anchored under the active input.
  var box = null, anchor = null, timer = null;
  function closeBox() { if (box) { box.remove(); box = null; anchor = null; } }
  function openBox(input, items) {
    closeBox();
    if (!items.length || document.activeElement !== input) { return; }
    box = document.createElement("ul");
    box.className = "gal-suggest";
    items.forEach(function (it) {
      var li = document.createElement("li");
      li.textContent = it.display && it.display !== it.address
        ? it.display + " <" + it.address + ">" : it.address;
      // mousedown fires before the input's blur, so the pick lands before close.
      li.addEventListener("mousedown", function (e) {
        e.preventDefault();
        replaceToken(input, currentToken(input), it.address);
        closeBox();
      });
      box.appendChild(li);
    });
    input.parentNode.appendChild(box);
    anchor = input;
  }
  function suggest(input) {
    var tok = currentToken(input);
    if (tok.text === "") { closeBox(); return; }
    fetch("/resolve?q=" + encodeURIComponent(tok.text), { credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (j) { if (j) { openBox(input, j.suggestions || []); } })
      .catch(function () {});
  }

  fields.forEach(function (input) {
    // Wrap the input so the absolutely-positioned dropdown anchors to it.
    var wrap = document.createElement("div");
    wrap.className = "rcpt-wrap";
    input.parentNode.insertBefore(wrap, input);
    wrap.appendChild(input);
    input.setAttribute("autocomplete", "off");

    input.addEventListener("input", function () {
      if (timer) { clearTimeout(timer); }
      timer = setTimeout(function () { suggest(input); }, 180);
    });
    // Delay close on blur so a suggestion click (mousedown) is processed first.
    input.addEventListener("blur", function () { setTimeout(closeBox, 150); });
    input.addEventListener("keydown", function (e) { if (e.key === "Escape") { closeBox(); } });
  });

  // "Check names": resolve every recipient field through the server, rewriting
  // the unambiguous matches in place and reporting the rest.
  var btn = document.getElementById("checknames");
  var status = document.getElementById("checknames-status");
  if (btn) {
    btn.addEventListener("click", function () {
      var problems = [], pending = fields.length;
      function done() { if (--pending === 0) { report(problems); } }
      fields.forEach(function (input) {
        if (!input.value.trim()) { done(); return; }
        fetch("/resolve?check=" + encodeURIComponent(input.value), { credentials: "same-origin" })
          .then(function (r) { return r.ok ? r.json() : null; })
          .then(function (j) { if (j && j.results) { applyCheck(input, j.results, problems); } })
          .catch(function () {})
          .then(done);
      });
    });
  }
  function applyCheck(input, results, problems) {
    var out = [];
    results.forEach(function (res) {
      if (res.status === "resolved" && res.matches && res.matches.length === 1) {
        out.push(res.matches[0].address);
        return;
      }
      out.push(res.input);
      if (res.status === "ambiguous") {
        problems.push(res.input + " (ambiguous: " + res.matches.map(function (m) { return m.address; }).join(", ") + ")");
      } else if (res.status === "unresolved") {
        problems.push(res.input + " (no match)");
      }
    });
    input.value = out.join(", ");
  }
  function report(problems) {
    if (!status) { return; }
    if (problems.length) {
      status.textContent = "Could not resolve: " + problems.join("; ");
      status.className = "checknames-status error";
    } else {
      status.textContent = "All recipients resolved.";
      status.className = "checknames-status ok";
    }
  }
})();
