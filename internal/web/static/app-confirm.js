// Netsession Relay UI: the confirmation dialog, the emergency stop and the forms that ask first.
(function () {
  "use strict";
  var N = window.NSR, $ = N.$, all = N.all, t = N.t;
  // ---- confirmation dialog (native <dialog>: focus stays inside, Esc closes) ----
  var dlg = $("confirm");
  function confirmBox(d, onOK) {
    if (!dlg || !dlg.showModal) { if (window.confirm(d.confirm)) onOK(); return; }
    $("confirm-title").textContent = d.confirmTitle || t("Sicher?");
    $("confirm-text").textContent = d.confirm || "";
    $("confirm-ok").textContent = d.confirmOk || t("Ja, weiter");
    dlg.returnValue = "cancel";
    dlg.onclose = function () { if (dlg.returnValue === "ok") onOK(); };
    dlg.showModal();
    $("confirm-cancel").focus();
  }
  // the emergency stop in the header; the theme "98" has a second one in its Start menu
  var stopLinks = all("#stop-link, [data-stop-link]"), stopForm = $("stop-form"), resumeLinks = all("#resume-link, [data-resume-link]");
  if (stopForm) {
    stopLinks.forEach(function (l) {
      l.addEventListener("click", function (e) {
        e.preventDefault();
        confirmBox(stopForm.dataset, function () { stopForm.submit(); });
      });
    });
  }
  all("form[data-confirm]").forEach(function (f) {
    if (f === stopForm || f.classList.contains("toggle-form")) return;
    f.addEventListener("submit", function (e) {
      if (f.dataset.confirmed) return;
      e.preventDefault();
      confirmBox(f.dataset, function () { f.dataset.confirmed = "1"; f.submit(); });
    });
  });

  N.dlg = dlg;
  N.confirmBox = confirmBox;
  N.stopLinks = stopLinks;
  N.resumeLinks = resumeLinks;
})();
