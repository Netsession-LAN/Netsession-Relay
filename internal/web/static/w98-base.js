// Netsession Relay, theme "98": the window manager of the desktop shell (layout.html "w98-shell", theme98.css).
// Every page is a program in its own window: an iframe of the same origin that gets the page without the desktop
// around it (server: Sec-Fetch-Dest "iframe"). Windows can be moved, resized, minimised, maximised and closed;
// the taskbar has one button per window. What is open is kept in sessionStorage, so a reload or a language
// switch brings the desktop back. Own code, nothing copied.
// This file finds or builds the desktop and holds the shared state and helpers in window.W98; w98-live.js,
// w98-win.js and w98.js (loaded after it) do nothing without it.
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
  var W = {
    doc: doc, desk: desk, tasks: tasks, startMenu: startMenu, winTpl: winTpl, taskTpl: taskTpl,
    SUFFIX: SUFFIX, ICONS: ICONS, MAXWINS: MAXWINS, MAXLIVE: MAXLIVE,
    wins: [],     // open windows in z-order, the last one is on top
    active: null, // the window with the blue title bar, null when all are minimised or closed
    opened: 0,    // counts windows for the cascade
    phone: window.matchMedia("(pointer: coarse), (max-width: 700px)")
  };

  function all(sel, root) { return Array.prototype.slice.call((root || doc).querySelectorAll(sel)); }
  // a plain left click: with a modifier key or another button the browser opens the link its own way
  var MODIFIERS = ["metaKey", "ctrlKey", "shiftKey", "altKey"];
  function plainClick(e) { return e.button === 0 && !MODIFIERS.some(function (k) { return e[k]; }); }
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
  function isPhone() { return W.phone.matches; }
  function area() { return { w: desk.clientWidth, h: desk.clientHeight }; }

  // ---- storage: best effort, the desktop works without ----
  function save() {
    try {
      sessionStorage.setItem(KEY, JSON.stringify({
        opened: W.opened,
        active: W.wins.indexOf(W.active),
        wins: W.wins.map(function (w) { return { url: w.url, x: w.x, y: w.y, w: w.w, h: w.h, min: w.min, max: w.max }; })
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
    var a = area(), left = a.w >= 1100 ? 100 : 8, n = W.opened % 6;
    return { x: left + n * 28, y: 8 + n * 26, w: Math.min(980, a.w - left - 16), h: Math.min(700, a.h - 24) };
  }

  W.all = all;
  W.plainClick = plainClick;
  W.parse = parse;
  W.clean = clean;
  W.appOf = appOf;
  W.isPhone = isPhone;
  W.area = area;
  W.save = save;
  W.load = load;
  W.place = place;
  W.firstPlace = firstPlace;
  window.W98 = W;
})();
