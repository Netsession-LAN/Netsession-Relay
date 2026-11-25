// Netsession Relay UI: small form helpers: upload size, save on change, copy button, fields shown by a select.
(function () {
  "use strict";
  var N = window.NSR, doc = N.doc, $ = N.$, all = N.all, t = N.t, toast = N.toast;
  // uploads: a file that is too large is refused here, before the browser sends it
  all("form[data-upload]").forEach(function (f) {
    f.addEventListener("submit", function (e) {
      var inp = f.querySelector("input[type=file][data-max]"), file = inp && inp.files && inp.files[0];
      if (file && file.size > Number(inp.dataset.max)) { e.preventDefault(); toast(inp.dataset.maxText, "danger"); }
    });
  });

  // a select that saves on change (profile of a game); the button stays for the form without JavaScript
  all("form[data-autosubmit]").forEach(function (f) {
    var btn = f.querySelector("button[type=submit]");
    if (btn) btn.hidden = true;
    f.addEventListener("change", function () { f.submit(); });
  });

  // ---- copy button (monitoring token); hidden without JavaScript, the text is selectable anyway.
  //      The UI runs on plain HTTP in the MGMT network, where browsers block navigator.clipboard (secure
  //      context only). Fallback: a temporary textarea + execCommand("copy"), which still works there inside
  //      a click. Only if both fail is the text just selected. ----
  all("[data-copy]").forEach(function (btn) {
    var src = $(btn.dataset.copy);
    if (!src) return;
    btn.hidden = false;
    btn.addEventListener("click", function () {
      var text = src.textContent.trim();
      var done = function () { toast(btn.dataset.copyDone || t("Kopiert.")); };
      var mark = function () {
        var r = doc.createRange();
        r.selectNodeContents(src);
        var s = window.getSelection();
        s.removeAllRanges();
        s.addRange(r);
        toast(t("Kopieren ging nicht. Der Text ist markiert: Strg+C drücken."), "warn");
      };
      var legacy = function () {
        var ta = doc.createElement("textarea");
        ta.value = text;
        ta.setAttribute("readonly", "");
        ta.style.position = "fixed";
        ta.style.top = "0";
        ta.style.left = "0";
        ta.style.opacity = "0";
        doc.body.appendChild(ta);
        ta.focus();
        ta.select();
        var ok = false;
        try { ok = doc.execCommand("copy"); } catch (e) { ok = false; }
        doc.body.removeChild(ta);
        btn.focus();
        if (ok) done(); else mark();
      };
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(done, legacy);
      } else legacy();
    });
  });

  // ---- a select that shows a field only for one value (multicast group), optionally another field for
  //      all other values (data-toggles-else); hidden fields are not sent ----
  all("select[data-toggles]").forEach(function (sel) {
    var field = $(sel.dataset.toggles), other = sel.dataset.togglesElse ? $(sel.dataset.togglesElse) : null;
    if (!field) return;
    var show = function (f, on) {
      if (!f) return;
      f.hidden = !on;
      all("input", f).forEach(function (i) { i.disabled = !on; });
    };
    var sync = function () {
      var on = sel.value === sel.dataset.togglesOn;
      show(field, on);
      show(other, !on);
    };
    sel.addEventListener("change", sync);
    sync();
  });
})();
