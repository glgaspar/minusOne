// Dialogs: [data-open-dialog="<id>"] opens, [data-close-dialog] or a click on the backdrop closes.
document.addEventListener("click", (e) => {
  const opener = e.target.closest("[data-open-dialog]");
  if (opener) {
    const dialog = document.getElementById(opener.dataset.openDialog);
    dialog.showModal();
    dialog.querySelector("[autofocus]")?.focus();
    return;
  }
  if (e.target.closest("[data-close-dialog]")) {
    e.target.closest("dialog")?.close();
    return;
  }
  // the dialog's content fills it, so a click landing on the dialog itself is on the backdrop
  if (e.target instanceof HTMLDialogElement) {
    e.target.close();
  }
});

// Sent by the server (HX-Trigger) after a song is added.
document.addEventListener("songAdded", () => {
  document.getElementById("add-song")?.close();
  toast("Song added to the catalog.");
});

// Sent by the server (HX-Trigger: {"showToast": {"message": ..., "error": ...}}).
document.addEventListener("showToast", (e) => {
  toast(e.detail.message, e.detail.error);
});

// htmx doesn't swap error responses, so without this a failed request looks like nothing happened.
document.addEventListener("htmx:responseError", (e) => {
  toast(`Something went wrong (${e.detail.xhr.status}). Try again.`, true);
});
document.addEventListener("htmx:sendError", () => {
  toast("Can't reach the server.", true);
});

let toastTimer;
function toast(message, isError = false) {
  const el = document.getElementById("toast");
  if (!el) return;
  el.textContent = message;
  el.classList.toggle("toast-error", isError);
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (el.hidden = true), 4000);
}

// Every tab is a device with its own id, kept across reloads. The player
// belongs to a device, so this goes with every request that touches the queue.
function deviceId() {
  try {
    let id = sessionStorage.getItem("minusone.device");
    if (!id) {
      id = crypto.randomUUID();
      sessionStorage.setItem("minusone.device", id);
    }
    return id;
  } catch {
    // no storage: a reload makes this a new device
    window.minusoneDevice ??= crypto.randomUUID();
    return window.minusoneDevice;
  }
}

document.addEventListener("htmx:configRequest", (e) => {
  e.detail.headers["X-Device-Id"] = deviceId();
});
