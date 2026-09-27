// Admin panel behaviour that htmx does not supply. The browser's own alert,
// confirm and prompt dialogs are never used: they block the page and cannot be
// styled with the panel.
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
})();
