// Netsession Relay, theme "98": the window manager of the desktop shell (layout.html "w98-shell", theme98.css).
// Every page is a program in its own window: an iframe of the same origin that gets the page without the desktop
// around it (server: Sec-Fetch-Dest "iframe"). Windows can be moved, resized, minimised, maximised and closed;
// the taskbar has one button per window. What is open is kept in sessionStorage, so a reload or a language
// switch brings the desktop back. Own code, nothing copied.
(function () {
  "use strict";
  var doc = document, desk = doc.getElementById("w98-desktop");
  var tasks = doc.getElementById("w98-tasks"), startMenu = doc.getElementById("w98-start");
  var winTpl = doc.getElementById("w98-win-tpl"), taskTpl = doc.getElementById("w98-task-tpl");
  if (!desk) {
    // The server could not tell a page from a window (no Sec-Fetch-Dest over plain http) and sent the single-window
    // page; w98mode.js marked it. Inside a window there is nothing to do. At the top this page becomes the desktop:
    // its own window goes, the same address is loaded again into a real one.
    if (doc.documentElement.dataset.w98 !== "shell" || !winTpl || !startMenu) return;
    desk = doc.createElement("div");
    desk.className = "w98-desktop";
    desk.id = "w98-desktop";
    desk.dataset.start = location.pathname + location.search;
    var icons = doc.querySelector(".w98-desk");
    if (icons) desk.appendChild(icons);
    var wall = doc.querySelector(".w98-wall"); // the event logo, the picture on the desktop
    if (wall) desk.appendChild(wall);
    all("#w98-min, #w98-max, .w98-win, label.w98-task").forEach(function (el) { el.remove(); });
    tasks = doc.createElement("div");
    tasks.className = "w98-tasks";
    tasks.id = "w98-tasks";
    startMenu.insertAdjacentElement("afterend", tasks);
    doc.body.insertBefore(desk, doc.body.firstChild);
  }
  var KEY = "w98desk", SUFFIX = " · Netsession Relay", ICONS = "/static/icons98.svg#w-";
  var MAXWINS = 8;  // more windows than programs, a guard only
  var MAXLIVE = 3;  // windows with a live connection; with the taskbar's own that leaves room in the six per server
  var MINW = 320, MINH = 200;
  // first path segment -> program (the name of its icon)
  var APPS = { "": "overview", spiele: "games", spieler: "players", finder: "finder", netz: "network", verlauf: "history", system: "system", notaus: "system" };
  var wins = [];     // open windows in z-order, the last one is on top
  var active = null; // the window with the blue title bar, null when all are minimised or closed
  var opened = 0;    // counts windows for the cascade
  var phone = window.matchMedia("(pointer: coarse), (max-width: 700px)");

  function all(sel, root) { return Array.prototype.slice.call((root || doc).querySelectorAll(sel)); }
  function parse(url) {
    try { var u = new URL(url, location.origin); return u.origin === location.origin ? u : null; } catch (e) { return null; }
  }
  // the address of a page as it is remembered: local, without the one-time messages of a redirect and without #
  function clean(url) {
    var u = parse(url);
    if (!u) return "/";
    u.searchParams.delete("ok");
    u.searchParams.delete("fehler");
    return u.pathname + u.search;
  }
  function appOf(url) {
    var u = parse(url), seg = u ? u.pathname.split("/")[1] || "" : "?";
    return Object.prototype.hasOwnProperty.call(APPS, seg) ? APPS[seg] : "";
  }
  function isPhone() { return phone.matches; }
  function area() { return { w: desk.clientWidth, h: desk.clientHeight }; }

  // ---- storage: best effort, the desktop works without ----
  function save() {
    try {
      sessionStorage.setItem(KEY, JSON.stringify({
        opened: opened,
        active: wins.indexOf(active),
        wins: wins.map(function (w) { return { url: w.url, x: w.x, y: w.y, w: w.w, h: w.h, min: w.min, max: w.max }; })
      }));
    } catch (e) { /* storage blocked or full */ }
  }
  function load() {
    try {
      var s = JSON.parse(sessionStorage.getItem(KEY) || "null");
      return s && Array.isArray(s.wins) ? s : null;
    } catch (e) { return null; }
  }

  // ---- geometry ----
  function clamp(w) {
    var a = area();
    w.w = Math.round(Math.min(Math.max(w.w, MINW), Math.max(a.w, 1)));
    w.h = Math.round(Math.min(Math.max(w.h, MINH), Math.max(a.h, 1)));
    w.x = Math.round(Math.min(Math.max(w.x, 0), Math.max(a.w - w.w, 0)));
    w.y = Math.round(Math.min(Math.max(w.y, 0), Math.max(a.h - w.h, 0)));
  }
  function place(w) {
    clamp(w);
    w.el.style.left = w.x + "px";
    w.el.style.top = w.y + "px";
    w.el.style.width = w.w + "px";
    w.el.style.height = w.h + "px";
  }
  function firstPlace() {
    var a = area(), left = a.w >= 1100 ? 100 : 8, n = opened % 6;
    return { x: left + n * 28, y: 8 + n * 26, w: Math.min(980, a.w - left - 16), h: Math.min(700, a.h - 24) };
  }

  // ---- what the desktop shows ----
  function topVisible(except) {
    for (var i = wins.length - 1; i >= 0; i--) if (!wins[i].min && wins[i] !== except) return wins[i];
    return null;
  }
  function chrome(w) {
    var icon = ICONS + (w.app || "relay");
    w.el.querySelector(".w98-title-text").textContent = w.title + " – Netsession Relay";
    w.el.querySelector(".w98-title use").setAttribute("href", icon);
    w.frame.title = w.title;
    w.task.querySelector("span").textContent = w.title;
    w.task.querySelector("use").setAttribute("href", icon);
    w.task.title = w.title;
  }
  function render() {
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
    doc.title = (active ? active.title : "Desktop") + SUFFIX;
    all("a[data-back]").forEach(function (a) {
      var u = parse(a.href);
      if (!u) return;
      u.searchParams.set("zurueck", active ? active.url : "/");
      a.href = u.pathname + u.search;
    });
    save();
    syncLive();
  }

  // ---- live connections: only windows that can be seen keep one (browsers allow six per server) ----
  function covers(o, w) {
    if (o.max || isPhone()) return true;
    return o.x <= w.x && o.y <= w.y && o.x + o.w >= w.x + w.w && o.y + o.h >= w.y + w.h;
  }
  function liveSet() {
    var live = [];
    for (var i = wins.length - 1; i >= 0 && live.length < MAXLIVE; i--) {
      var w = wins[i];
      if (w.min) continue;
      if (isPhone() && w !== active) continue;
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

  // ---- window actions ----
  function focus(w) {
    var i = wins.indexOf(w);
    if (i < 0) return;
    wins.splice(i, 1);
    wins.push(w);
    w.min = false;
    active = w;
    render();
  }
  function minimise(w) {
    w.min = true;
    if (active === w) active = isPhone() ? null : topVisible(w);
    render();
  }
  function toggleMax(w) {
    w.max = !w.max;
    focus(w);
  }
  function close(w) {
    var i = wins.indexOf(w);
    if (i < 0) return;
    wins.splice(i, 1);
    w.el.remove();
    w.task.remove();
    if (active === w) active = isPhone() ? null : topVisible(null);
    render();
  }
  function navigate(w, url) {
    w.url = clean(url);
    w.app = appOf(url);
    w.frame.src = url;
  }
  function closeStart() { if (startMenu && startMenu.open) startMenu.open = false; }

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
    chrome(w);
    // clicks inside the page do not reach the desktop: listen in the page for focus and for links
    cw.document.addEventListener("pointerdown", function () {
      closeStart();
      if (active !== w) focus(w);
    }, true);
    cw.document.addEventListener("click", function (e) {
      var a = e.target && e.target.closest ? e.target.closest("a[href]") : null;
      if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      if (a.target || a.hasAttribute("download")) return;
      var u = parse(a.href), app = u ? appOf(u.href) : "";
      // a link into another program opens or focuses that program; everything else stays in this window
      if (!u || !app || app === w.app) return;
      e.preventDefault();
      openApp(u.pathname + u.search + u.hash, true);
    });
    render();
  }

  // ---- moving and resizing: pointer events with capture, so the pointer may run over the pages ----
  function track(handle, w, start, move) {
    handle.addEventListener("pointerdown", function (e) {
      if (e.button !== 0 || e.target.closest(".w98-tb")) return;
      closeStart();
      focus(w);
      if (w.max || isPhone()) return;
      var from = start(), px = e.clientX, py = e.clientY;
      e.preventDefault();
      try { handle.setPointerCapture(e.pointerId); } catch (err) { /* pointer gone */ }
      desk.classList.add("is-dragging");
      function onMove(ev) { move(from, ev.clientX - px, ev.clientY - py); place(w); }
      function onEnd() {
        handle.removeEventListener("pointermove", onMove);
        handle.removeEventListener("pointerup", onEnd);
        handle.removeEventListener("pointercancel", onEnd);
        desk.classList.remove("is-dragging");
        render();
      }
      handle.addEventListener("pointermove", onMove);
      handle.addEventListener("pointerup", onEnd);
      handle.addEventListener("pointercancel", onEnd);
    });
  }

  function create(s) {
    var el = winTpl.content.firstElementChild.cloneNode(true);
    var task = taskTpl.content.firstElementChild.cloneNode(true);
    var w = { el: el, task: task, frame: el.querySelector("iframe"), url: clean(s.url), app: appOf(s.url), title: "…",
      x: s.x, y: s.y, w: s.w, h: s.h, min: !!s.min, max: !!s.max, live: null };
    if (!(w.w > 0 && w.h > 0)) { var p = firstPlace(); w.x = p.x; w.y = p.y; w.w = p.w; w.h = p.h; }
    opened++;
    var bar = el.querySelector(".w98-title");
    track(bar, w, function () { return { x: w.x, y: w.y }; }, function (from, dx, dy) { w.x = from.x + dx; w.y = from.y + dy; });
    track(el.querySelector(".w98-grip"), w, function () { return { w: w.w, h: w.h }; }, function (from, dx, dy) {
      var a = area();
      w.w = Math.min(from.w + dx, a.w - w.x);
      w.h = Math.min(from.h + dy, a.h - w.y);
    });
    bar.addEventListener("dblclick", function (e) { if (!e.target.closest(".w98-tb") && !isPhone()) toggleMax(w); });
    el.addEventListener("click", function (e) {
      var b = e.target.closest("[data-act]");
      if (!b) return;
      if (b.dataset.act === "min") minimise(w);
      else if (b.dataset.act === "max") toggleMax(w);
      else close(w);
    });
    task.addEventListener("click", function () {
      closeStart();
      if (active === w && !w.min) minimise(w); else focus(w);
    });
    w.frame.addEventListener("load", function () { loaded(w); });
    wins.push(w);
    place(w);
    chrome(w);
    w.frame.src = s.url;
    desk.appendChild(el);
    tasks.appendChild(task);
    return w;
  }

  // open a program, or bring its window to the front. go: also take the window to this very address
  function openApp(url, go) {
    var target = clean(url), app = appOf(url), w = null;
    wins.forEach(function (x) { if (!w && (app ? x.app === app : x.url === target)) w = x; });
    if (!w && wins.length >= MAXWINS) { w = wins[0]; go = true; }
    if (!w) w = create({ url: url });
    else if (go && (w.url !== target || url !== target)) navigate(w, url);
    focus(w);
    return w;
  }

  // ---- desktop icons, Start menu, tray: program links open windows ----
  doc.addEventListener("click", function (e) {
    var a = e.target.closest ? e.target.closest("a[data-app]") : null;
    if (!a || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    closeStart();
    openApp(a.getAttribute("href"), false);
  });

  window.addEventListener("resize", function () { wins.forEach(place); render(); });
  if (phone.addEventListener) phone.addEventListener("change", render);

  // ---- start: the windows of last time, then the page that was asked for ----
  var saved = load();
  if (saved) {
    opened = saved.opened > 0 ? saved.opened : 0;
    saved.wins.slice(0, MAXWINS).forEach(function (s) { if (s && typeof s.url === "string") create(s); });
    active = wins[saved.active] && !wins[saved.active].min ? wins[saved.active] : null;
  }
  if (location.hash !== "#desktop") openApp(desk.dataset.start || "/", true);
  else render();
})();
