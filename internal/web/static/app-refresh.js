// Netsession Relay UI: pages with a server-rendered fragment that refreshes every 5 s (players, port finder).
// The swap must not disturb someone reading: opened <details data-key> stay open, keyboard focus stays
// on the same link or summary, and nothing is replaced while text inside is selected (copying bytes).
(function () {
  "use strict";
  var N = window.NSR, doc = N.doc, all = N.all, localTimes = N.localTimes;
  function focusMark(box) {
    var a = doc.activeElement;
    if (!a || !box.contains(a)) return null;
    if (a.tagName === "SUMMARY" && a.parentNode.dataset.key) return { key: a.parentNode.dataset.key };
    if (a.getAttribute("href")) return { href: a.getAttribute("href") };
    return null;
  }
  all("[data-refresh]").forEach(function (box) {
    var last = null;
    setInterval(function () {
      if (doc.hidden) return;
      fetch(box.dataset.refresh, { credentials: "same-origin" }).then(function (r) { return r.ok ? r.text() : null; }).then(function (html) {
        if (html === null || html === last) return;
        var sel = window.getSelection && window.getSelection();
        if (sel && !sel.isCollapsed && sel.anchorNode && box.contains(sel.anchorNode)) return;
        var open = all("details[open][data-key]", box).map(function (d) { return d.dataset.key; });
        var mark = focusMark(box);
        last = html;
        box.innerHTML = html;
        localTimes(box);
        all("details[data-key]", box).forEach(function (d) {
          if (open.indexOf(d.dataset.key) >= 0) d.open = true;
          if (mark && mark.key === d.dataset.key) d.querySelector("summary").focus();
        });
        if (mark && mark.href) all("a[href]", box).some(function (a) {
          if (a.getAttribute("href") !== mark.href) return false;
          a.focus();
          return true;
        });
      }).catch(function () {});
    }, 5000);
  });
})();
