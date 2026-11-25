// Netsession Relay UI: switches and the profile matrix apply without reloading the page.
(function () {
  "use strict";
  var N = window.NSR, doc = N.doc, csrf = N.csrf, all = N.all, setText = N.setText, t = N.t, toast = N.toast, confirmBox = N.confirmBox;
  // ---- switches: apply without reloading the page ----
  function setSwitch(f, on) {
    var btn = f.querySelector(".switch");
    btn.setAttribute("aria-checked", on ? "true" : "false");
    setText(btn.querySelector(".switch-word"), on ? t("An") : t("Aus"));
    f.querySelector('input[name="on"]').value = on ? "0" : "1";
    var row = f.closest("[data-on]");
    if (row) row.dataset.on = on ? "1" : "0";
  }
  function send(f) {
    var btn = f.querySelector(".switch");
    btn.classList.add("busy");
    btn.setAttribute("aria-busy", "true");
    fetch(f.action, {
      method: "POST",
      headers: { "Accept": "application/json", "X-CSRF-Token": csrf },
      body: new FormData(f),
      credentials: "same-origin"
    }).then(function (r) { return r.json(); }).then(function (a) {
      if (a.error) { toast(a.error, "danger"); return; }
      setSwitch(f, a.enabled);
      var msg = a.message || t("Angewendet.");
      if (a.warnings && a.warnings.length) toast(msg + t(" Hinweis: ") + a.warnings.join(" · "), "warn");
      else toast(msg);
      doc.dispatchEvent(new Event("relay:changed"));
    }).catch(function () {
      toast(t("Keine Antwort vom Relay-Server. Der Schalter wurde nicht geändert."), "danger");
    }).then(function () {
      btn.classList.remove("busy");
      btn.removeAttribute("aria-busy");
    });
  }
  all("form.toggle-form").forEach(function (f) {
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      var turningOff = f.querySelector('input[name="on"]').value === "0";
      if (f.dataset.confirm && turningOff) confirmBox(f.dataset, function () { send(f); });
      else send(f);
    });
  });

  // ---- profile matrix: with JavaScript a tick applies at once (one edge), the save button is for the form without it ----
  all("form[data-edge]").forEach(function (f) {
    var save = f.querySelector("[data-save]");
    if (save) save.hidden = true;
    var note = f.querySelector("[data-live-note]");
    if (note) note.hidden = false;
    f.addEventListener("change", function (e) {
      var box = e.target;
      if (!box || box.name !== "e") return;
      var parts = box.value.split(">"), cell = box.closest(".cell"), want = box.checked;
      var run = function () {
        var fd = new FormData();
        fd.append("csrf", csrf); fd.append("src", parts[0]); fd.append("dst", parts[1]); fd.append("on", want ? "1" : "0");
        if (cell) cell.classList.add("busy");
        fetch(f.dataset.edge, { method: "POST", headers: { "Accept": "application/json", "X-CSRF-Token": csrf }, body: fd, credentials: "same-origin" })
          .then(function (r) { return r.json(); }).then(function (a) {
            if (a.error) { box.checked = !want; toast(a.error, "danger"); return; }
            if (a.warnings && a.warnings.length) toast((a.message || t("Angewendet.")) + t(" Hinweis: ") + a.warnings.join(" · "), "warn");
            else toast(a.message || t("Angewendet."));
          }).catch(function () {
            box.checked = !want;
            toast(t("Keine Antwort vom Relay-Server. Der Haken wurde nicht geändert."), "danger");
          }).then(function () { if (cell) cell.classList.remove("busy"); });
      };
      // opening the games to WiFi or the crew network is worth a second look
      if (want && box.dataset.ask) {
        box.checked = false;
        confirmBox({ confirmTitle: box.dataset.askTitle, confirm: box.dataset.ask, confirmOk: t("Einschalten") }, function () { box.checked = true; run(); });
      } else run();
    });
  });

  N.setSwitch = setSwitch;
})();
