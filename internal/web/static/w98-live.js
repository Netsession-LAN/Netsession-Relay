// Netsession Relay, theme "98": what the desktop shows (title bars, taskbar, address bar) and which windows keep
// a live connection. Needs w98-base.js.
(function () {
  "use strict";
  var W = window.W98;
  if (!W) return;
  var doc = W.doc, desk = W.desk, wins = W.wins, all = W.all, parse = W.parse, isPhone = W.isPhone;

  // ---- what the desktop shows ----
  function topVisible(except) {
    for (var i = wins.length - 1; i >= 0; i--) if (!wins[i].min && wins[i] !== except) return wins[i];
    return null;
  }
  function chrome(w) {
    var icon = W.ICONS + (w.app || "relay");
    w.el.querySelector(".w98-title-text").textContent = w.title + " – Netsession Relay";
    w.el.querySelector(".w98-title use").setAttribute("href", icon);
    w.frame.title = w.title;
    w.task.querySelector("span").textContent = w.title;
    w.task.querySelector("use").setAttribute("href", icon);
    w.task.title = w.title;
  }
  function render() {
    var active = W.active;
    wins.forEach(function (w, i) {
      w.el.style.zIndex = String(i + 1);
      w.el.classList.toggle("is-active", w === active);
      w.el.classList.toggle("is-min", w.min);
      w.el.classList.toggle("is-max", w.max);
      var mx = w.el.querySelector('[data-act="max"]'), label = w.max ? mx.dataset.restore : mx.dataset.max;
      mx.title = label;
      mx.setAttribute("aria-label", label);
      w.task.classList.toggle("is-active", w === active);
      w.task.setAttribute("aria-pressed", w === active ? "true" : "false");
    });
    desk.classList.toggle("is-phone", isPhone());
    // the address bar and the tab follow the focused window, so a reload or a bookmark comes back to it;
    // "#desktop" says that nothing is in front (all windows minimised or closed)
    var here = active ? active.url : "/#desktop";
    try { history.replaceState(null, "", here); } catch (e) { /* not allowed, e.g. in a sandbox */ }
    doc.title = (active ? active.title : "Desktop") + W.SUFFIX;
    all("a[data-back]").forEach(function (a) {
      var u = parse(a.href);
      if (!u) return;
      u.searchParams.set("zurueck", active ? active.url : "/");
      a.href = u.pathname + u.search;
    });
    W.save();
    syncLive();
  }

  // ---- live connections: only windows that can be seen keep one (browsers allow six per server) ----
  function covers(o, w) {
    if (o.max || isPhone()) return true;
    return o.x <= w.x && o.y <= w.y && o.x + o.w >= w.x + w.w && o.y + o.h >= w.y + w.h;
  }
  function liveSet() {
    var live = [];
    for (var i = wins.length - 1; i >= 0 && live.length < W.MAXLIVE; i--) {
      var w = wins[i];
      if (w.min) continue;
      if (isPhone() && w !== W.active) continue;
      var hidden = false;
      for (var j = i + 1; j < wins.length; j++) if (!wins[j].min && covers(wins[j], w)) { hidden = true; break; }
      if (!hidden) live.push(w);
    }
    return live;
  }
  function tell(w, on) {
    w.live = on;
    try { w.frame.contentWindow.postMessage({ w98: "live", on: on }, location.origin); } catch (e) { /* frame not ready */ }
  }
  function syncLive() {
    var live = liveSet();
    wins.forEach(function (w) {
      var on = live.indexOf(w) >= 0;
      if (w.live !== on) tell(w, on);
    });
  }
  // a page that has loaded asks whether it may connect
  window.addEventListener("message", function (e) {
    if (e.origin !== location.origin || !e.data || e.data.w98 !== "ready") return;
    wins.forEach(function (w) {
      if (w.frame.contentWindow === e.source) tell(w, liveSet().indexOf(w) >= 0);
    });
  });

  W.topVisible = topVisible;
  W.chrome = chrome;
  W.render = render;
})();
