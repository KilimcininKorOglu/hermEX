// Applies the admin panel's theme before the first paint, so a dark-theme page
// never flashes light. The operator's choice lives in the admin_theme cookie;
// without one the page follows the system setting and keeps following it.
(function () {
  "use strict";
  const media = window.matchMedia("(prefers-color-scheme: dark)");
  const apply = () => {
    const chosen = /(?:^|;\s*)admin_theme=(light|dark)(?:;|$)/.exec(document.cookie);
    document.documentElement.dataset.theme = chosen ? chosen[1] : (media.matches ? "dark" : "light");
  };
  apply();
  media.addEventListener("change", apply);
})();
