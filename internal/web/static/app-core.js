// Netsession Relay UI: progressive enhancement only. Every form also works without JavaScript.
// This file holds the shared helpers in window.NSR; the parts (app-*.js, loaded after it) take what they need.
(function () {
  "use strict";
  var doc = document, body = doc.body;
  var csrf = body.dataset.csrf || "";
  function $(id) { return doc.getElementById(id); }
  function all(sel, root) { return Array.prototype.slice.call((root || doc).querySelectorAll(sel)); }
  function num(n) { return String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, " "); }
  function setText(el, text) { if (el && el.textContent !== text) el.textContent = text; }
  // ---- texts: German in the source, English from the catalog the page embeds (#i18n); %s is replaced in order ----
  var catalog = {};
  try { catalog = JSON.parse(($("i18n") || {}).textContent || "{}"); } catch (e) { catalog = {}; }
  function t(s) {
    var out = catalog[s] || s, args = Array.prototype.slice.call(arguments, 1);
    return out.replace(/%s/g, function () { return args.length ? String(args.shift()) : ""; });
  }
  function announce(text) { var a = $("announce"); if (a) { a.textContent = ""; setTimeout(function () { a.textContent = text; }, 50); } }

  // ---- times: the server writes UTC, show the browser's local time ----
  function two(n) { return (n < 10 ? "0" : "") + n; }
  function fmtTime(d, kind) {
    var hm = two(d.getHours()) + ":" + two(d.getMinutes());
    if (kind === "time") return hm + ":" + two(d.getSeconds());
    return two(d.getDate()) + "." + two(d.getMonth() + 1) + ". " + hm;
  }
  function localTimes(root) {
    all("time[datetime]", root).forEach(function (el) {
      var d = new Date(el.getAttribute("datetime"));
      if (!isNaN(d)) el.textContent = fmtTime(d, el.dataset.fmt);
    });
  }
  localTimes(doc);

  // ---- toast ----
  var toastTimer;
  function toast(text, kind) {
    var box = $("toast");
    if (!box) return;
    $("toast-text").textContent = text;
    box.dataset.tone = kind || "ok";
    var use = box.querySelector("#toast-icon use");
    if (use) use.setAttribute("href", "/static/icons.svg#i-" + (kind ? "alert" : "check"));
    box.hidden = false;
    clearTimeout(toastTimer);
    // errors stay until closed, confirmations leave by themselves
    if (kind !== "danger") toastTimer = setTimeout(function () { box.hidden = true; }, kind === "warn" ? 9000 : 5000);
  }
  if ($("toast-close")) $("toast-close").addEventListener("click", function () { $("toast").hidden = true; });

  // ---- building elements for the live updates ----
  function icon(name) {
    var ns = "http://www.w3.org/2000/svg";
    var svg = doc.createElementNS(ns, "svg"), use = doc.createElementNS(ns, "use");
    svg.setAttribute("class", "ic");
    svg.setAttribute("aria-hidden", "true");
    use.setAttribute("href", "/static/icons.svg#i-" + name);
    svg.appendChild(use);
    return svg;
  }
  function el(tag, cls, text) {
    var e = doc.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  window.NSR = {
    doc: doc, body: body, csrf: csrf, $: $, all: all, num: num, setText: setText, t: t, announce: announce,
    fmtTime: fmtTime, localTimes: localTimes, toast: toast, icon: icon, el: el,
    prev: {} // what the live parts (app-live-*.js) showed last, by key
  };
})();
