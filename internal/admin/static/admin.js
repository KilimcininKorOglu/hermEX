// Admin panel behaviour that htmx does not supply: the confirm dialog, toasts,
// the theme switch and the narrow-screen menu. The browser's own alert, confirm
// and prompt dialogs are never used: they block the page and cannot be styled
// with the panel.
(function () {
  "use strict";

  // askConfirm opens the page's confirm dialog with question and resolves
  // whether the operator confirmed. Cancel and Escape answer no. A page without
  // the dialog answers no, so a destructive request is never sent unasked.
  function askConfirm(question) {
    const dialog = document.getElementById("confirm-dialog");
    if (!dialog || typeof dialog.showModal !== "function") {
      return Promise.resolve(false);
    }
    dialog.querySelector(".confirm-message").textContent = question;
    dialog.returnValue = "";
    return new Promise((resolve) => {
      dialog.addEventListener("close", () => resolve(dialog.returnValue === "confirm"), { once: true });
      dialog.showModal();
    });
  }

  // htmx asks before a request that carries hx-confirm; the question is taken
  // over here and the request is issued only on a yes.
  document.addEventListener("htmx:confirm", (evt) => {
    const question = evt.detail.question;
    if (!question) {
      return;
    }
    evt.preventDefault();
    askConfirm(question).then((ok) => {
      if (ok) {
        evt.detail.issueRequest(true);
      }
    });
  });

  // pageText returns a message the page rendered in the operator's language onto
  // the root element as data-msg-<name>, with {0} replaced by arg.
  function pageText(name, arg) {
    const key = "msg" + name.replace(/(^|-)(\w)/g, (_, _dash, c) => c.toUpperCase());
    const message = document.documentElement.dataset[key] || "";
    return arg === undefined ? message : message.replace("{0}", String(arg));
  }

  // How long a toast stays, by kind. An error stays longer so it can be read.
  const TOAST_MS = { ok: 3500, warn: 6000, error: 8000 };

  // glyph copies one icon out of the page's toast-icons template, whose <use>
  // references carry the sprite's versioned URL.
  function glyph(name) {
    const tpl = document.getElementById("toast-icons");
    const svg = tpl && tpl.content.querySelector('[data-kind="' + name + '"]');
    return svg ? svg.cloneNode(true) : document.createTextNode("");
  }

  // toast shows text in the corner of the page for a while.
  function toast(kind, text) {
    const region = document.getElementById("toasts");
    if (!region || !text) {
      return;
    }
    const item = document.createElement("div");
    item.className = "toast toast-" + kind;
    item.setAttribute("role", kind === "error" ? "alert" : "status");
    const body = document.createElement("p");
    body.textContent = text;
    const close = document.createElement("button");
    close.type = "button";
    close.setAttribute("aria-label", pageText("dismiss"));
    close.append(glyph("close"));
    close.addEventListener("click", () => item.remove());
    item.append(glyph(kind), body, close);
    region.append(item);
    setTimeout(() => item.remove(), TOAST_MS[kind]);
  }

  // kindOf names the toast kind a status message element carries.
  function kindOf(el) {
    if (el.classList.contains("ok")) {
      return "ok";
    }
    return el.classList.contains("warn") ? "warn" : "error";
  }

  // A status message an htmx swap brings in is announced as a toast, because
  // the form that caused it may sit far from where the message lands. A save
  // acknowledgement is not repeated inline; a warning or an error stays next to
  // the form as well.
  document.addEventListener("htmx:load", (evt) => {
    const root = evt.detail.elt;
    if (!(root instanceof Element) || root === document.body) {
      return;
    }
    // Only a paragraph is a message; a span with the same class is a status
    // badge inside a table (a DNS check result) and stays where it is.
    const selector = "p.ok, p.warn, p.error, p.err";
    const found = Array.from(root.querySelectorAll(selector));
    if (root.matches(selector)) {
      found.unshift(root);
    }
    for (const el of found) {
      const kind = kindOf(el);
      toast(kind, el.textContent.trim());
      if (kind !== "ok") {
        continue;
      }
      // A hidden element still counts as the sibling the spacing rules measure
      // from, so the acknowledgement is removed. The swapped root itself only
      // hides, because a later swap may still target it.
      if (el === root) {
        el.classList.add("toasted");
      } else {
        el.remove();
      }
    }
  });

  // A create form marked data-reset is cleared once what it created has landed,
  // so the next item starts from empty fields and a second click does not send
  // the same item again. A handler answers a refused create with the panel and
  // an error message in it, so the form keeps its values while that message
  // shows. The swap has replaced the target by now, so it is looked up again.
  document.addEventListener("htmx:afterRequest", (evt) => {
    const form = evt.detail.elt;
    if (!(form instanceof HTMLFormElement) || !form.hasAttribute("data-reset") || !evt.detail.successful) {
      return;
    }
    const target = evt.detail.target && evt.detail.target.id ? document.getElementById(evt.detail.target.id) : null;
    if (target && target.querySelector(".error, .err")) {
      return;
    }
    form.reset();
    // A reset returns a selector to its first choice, but the fields it fetched
    // for the choice before stay, so each such selector fetches them again.
    for (const select of form.querySelectorAll("select[hx-get]")) {
      select.dispatchEvent(new Event("change"));
    }
  });

  // htmx does not swap an error response, so without this a failed request
  // would change nothing on the page and say nothing.
  document.addEventListener("htmx:responseError", (evt) => {
    toast("error", pageText("request-failed", evt.detail.xhr.status));
  });
  document.addEventListener("htmx:sendError", () => {
    toast("error", pageText("unreachable"));
  });

  // savePref stores one interface preference in the users record webmail shares;
  // the answer also sets the cookie that caches it. It resolves on success and
  // rejects on any failure.
  async function savePref(name, value) {
    const csrf = /(?:^|;\s*)hermex_admin_csrf=([^;]*)/.exec(document.cookie);
    const resp = await fetch("/admin/ui/prefs", {
      method: "PUT",
      headers: { "Content-Type": "application/x-www-form-urlencoded", "X-CSRF-Token": csrf ? decodeURIComponent(csrf[1]) : "" },
      body: name + "=" + encodeURIComponent(value),
    });
    if (!resp.ok) {
      throw new Error("HTTP " + resp.status);
    }
  }

  // cachePref keeps a preference in the cookie the server reads it from, for the
  // sign-in page, where no session can store it yet. Signing in adopts it into
  // the users record when the record holds no choice.
  function cachePref(name, value) {
    document.cookie = "admin_" + name + "=" + encodeURIComponent(value) +
      "; Path=/admin; Max-Age=31536000; SameSite=Lax; Secure";
  }

  // localPrefs reports whether a control sits where preferences are cached only.
  function localPrefs(el) {
    return el.closest("[data-prefs-local]") !== null;
  }

  // setTheme shows the operator's theme at once and stores it. A failed save puts
  // the previous theme back and says so.
  function setTheme(theme, local) {
    const previous = document.documentElement.dataset.theme;
    document.documentElement.dataset.theme = theme;
    if (local) {
      cachePref("theme", theme);
      return;
    }
    savePref("theme", theme).catch(() => {
      document.documentElement.dataset.theme = previous;
      toast("error", pageText("theme-failed"));
    });
  }

  // The language selector stores the choice and reloads the page, which the
  // server renders in the new language. A failed save puts the selector back.
  document.addEventListener("change", (evt) => {
    const select = evt.target instanceof HTMLSelectElement && evt.target.matches(".lang-select") ? evt.target : null;
    if (!select) {
      return;
    }
    if (localPrefs(select)) {
      cachePref("lang", select.value);
      window.location.reload();
      return;
    }
    savePref("lang", select.value).then(() => window.location.reload()).catch(() => {
      select.value = document.documentElement.lang;
      toast("error", pageText("lang-failed"));
    });
  });

  // setNav opens or closes the narrow-screen menu.
  function setNav(open) {
    document.body.classList.toggle("nav-open", open);
    const toggle = document.querySelector(".nav-toggle");
    if (toggle) {
      toggle.setAttribute("aria-expanded", String(open));
    }
  }

  document.addEventListener("click", (evt) => {
    const target = evt.target instanceof Element ? evt.target : null;
    if (!target) {
      return;
    }
    if (target.closest(".theme-toggle")) {
      setTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark", localPrefs(target));
    } else if (target.closest(".nav-toggle")) {
      setNav(!document.body.classList.contains("nav-open"));
    } else if (target.closest("[data-nav-close], .sidebar nav a")) {
      setNav(false);
    }
  });

  // The sidebar scrolls on its own; a page listed below its fold would open
  // with its own entry out of sight, so the active entry is brought into view.
  const activeNav = document.querySelector(".sidebar nav a.active");
  if (activeNav) {
    activeNav.scrollIntoView({ block: "nearest" });
  }

  // A help icon sits inside a label, and a click on a label toggles or focuses
  // its control. Reading the explanation must not change a checkbox.
  document.addEventListener("click", (evt) => {
    if (evt.target instanceof Element && evt.target.closest(".help")) {
      evt.preventDefault();
    }
  });

  document.addEventListener("keydown", (evt) => {
    if (evt.key === "Escape" && document.body.classList.contains("nav-open")) {
      setNav(false);
    }
  });

  // DURATION_UNITS are the steps a second count is spelled out in, largest first.
  const DURATION_UNITS = [
    ["unit-days", 86400],
    ["unit-hours", 3600],
    ["unit-minutes", 60],
    ["unit-seconds", 1],
  ];

  // spellDuration writes a second count in every unit it holds: 90061 reads as
  // "1 day 1 h 1 min 1 s". It returns "" for a value that is not a whole count.
  function spellDuration(value) {
    let left = Number(value);
    if (!Number.isInteger(left) || left <= 0) {
      return "";
    }
    const parts = [];
    for (const [name, size] of DURATION_UNITS) {
      const n = Math.floor(left / size);
      left -= n * size;
      if (n > 0) {
        parts.push(pageText(name, n));
      }
    }
    return "= " + parts.join(" ");
  }

  // A field entered in seconds shows its value spelled out beside it, so 2592000
  // reads as 30 days while it is typed.
  function showDuration(input) {
    let hint = input.nextElementSibling;
    if (!hint || !hint.classList.contains("duration-hint")) {
      hint = document.createElement("small");
      hint.className = "duration-hint";
      input.after(hint);
    }
    hint.textContent = spellDuration(input.value);
  }
  function showDurations(root) {
    for (const input of root.querySelectorAll("input[data-duration]")) {
      showDuration(input);
    }
  }
  showDurations(document);
  document.addEventListener("htmx:load", (evt) => {
    if (evt.detail.elt instanceof Element) {
      showDurations(evt.detail.elt);
    }
  });
  document.addEventListener("input", (evt) => {
    if (evt.target instanceof HTMLInputElement && evt.target.hasAttribute("data-duration")) {
      showDuration(evt.target);
    }
  });

  // The account page's time zone field suggests every zone the browser knows,
  // and its button fills in the browser's own zone. The server checks the name.
  const zoneList = document.getElementById("tz-names");
  if (zoneList && typeof Intl.supportedValuesOf === "function") {
    for (const name of Intl.supportedValuesOf("timeZone")) {
      const option = document.createElement("option");
      option.value = name;
      zoneList.append(option);
    }
  }
  document.addEventListener("click", (evt) => {
    const button = evt.target instanceof Element ? evt.target.closest("[data-zone-browser]") : null;
    const input = button && button.form ? button.form.querySelector("[data-zone-input]") : null;
    if (input) {
      input.value = Intl.DateTimeFormat().resolvedOptions().timeZone;
    }
  });
})();
