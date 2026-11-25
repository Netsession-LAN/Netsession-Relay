// Netsession Relay, theme "98": pages loading in windows, program links, and the start of the desktop.
// Needs w98-base.js, w98-live.js and w98-win.js.
(function () {
  "use strict";
  var W = window.W98;
  if (!W) return;
  var doc = W.doc, desk = W.desk, wins = W.wins, SUFFIX = W.SUFFIX, MAXWINS = W.MAXWINS;
  var parse = W.parse, clean = W.clean, appOf = W.appOf, plainClick = W.plainClick, closeStart = W.closeStart, focus = W.focus;

  // ---- a page has loaded in a window ----
  function loaded(w) {
    var cw, loc, path;
    try { cw = w.frame.contentWindow; loc = cw.location; path = loc.pathname; } catch (e) { return; }
    if (loc.protocol === "about:") return;
    // the session ended: the login page belongs to the whole browser window, not into a small one
    if (path === "/login" || path.indexOf("/login/") === 0 || path.indexOf("/oidc/") === 0) { window.location.href = "/login"; return; }
    w.url = clean(loc.href);
    w.app = appOf(loc.href);
    var title = cw.document.title || "";
    w.title = title.slice(-SUFFIX.length) === SUFFIX ? title.slice(0, -SUFFIX.length) : title || w.url;
    W.chrome(w);
    // clicks inside the page do not reach the desktop: listen in the page for focus and for links
    cw.document.addEventListener("pointerdown", function () {
      closeStart();
      if (W.active !== w) focus(w);
    }, true);
    cw.document.addEventListener("click", function (e) {
      var a = e.target && e.target.closest ? e.target.closest("a[href]") : null;
      if (!a || e.defaultPrevented || !plainClick(e)) return;
      if (a.target || a.hasAttribute("download")) return;
      var u = parse(a.href), app = u ? appOf(u.href) : "";
      // a link into another program opens or focuses that program; everything else stays in this window
      if (!u || !app || app === w.app) return;
      e.preventDefault();
      openApp(u.pathname + u.search + u.hash, true);
    });
    W.render();
  }
  W.loaded = loaded;

  // open a program, or bring its window to the front. go: also take the window to this very address
  function openApp(url, go) {
    var target = clean(url), app = appOf(url), w = null;
    wins.forEach(function (x) { if (!w && (app ? x.app === app : x.url === target)) w = x; });
    if (!w && wins.length >= MAXWINS) { w = wins[0]; go = true; }
    if (!w) w = W.create({ url: url });
    else if (go && (w.url !== target || url !== target)) W.navigate(w, url);
    focus(w);
    return w;
  }

  // ---- desktop icons, Start menu, tray: program links open windows ----
  doc.addEventListener("click", function (e) {
    var a = e.target.closest ? e.target.closest("a[data-app]") : null;
    if (!a || !plainClick(e)) return;
    e.preventDefault();
    closeStart();
    openApp(a.getAttribute("href"), false);
  });

  window.addEventListener("resize", function () { wins.forEach(W.place); W.render(); });
  if (W.phone.addEventListener) W.phone.addEventListener("change", W.render);

  // ---- start: the windows of last time, then the page that was asked for ----
  var saved = W.load();
  if (saved) {
    W.opened = saved.opened > 0 ? saved.opened : 0;
    saved.wins.slice(0, MAXWINS).forEach(function (s) { if (s && typeof s.url === "string") W.create(s); });
    W.active = wins[saved.active] && !wins[saved.active].min ? wins[saved.active] : null;
  }
  if (location.hash !== "#desktop") openApp(desk.dataset.start || "/", true);
  else W.render();
})();
