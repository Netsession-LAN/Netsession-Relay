// Netsession Relay, theme "98": a window and what can be done with it (focus, minimise, maximise, close, move,
// resize). Needs w98-base.js and w98-live.js.
(function () {
  "use strict";
  var W = window.W98;
  if (!W) return;
  var desk = W.desk, wins = W.wins, startMenu = W.startMenu, isPhone = W.isPhone, render = W.render, place = W.place;

  // ---- window actions ----
  function focus(w) {
    var i = wins.indexOf(w);
    if (i < 0) return;
    wins.splice(i, 1);
    wins.push(w);
    w.min = false;
    W.active = w;
    render();
  }
  function minimise(w) {
    w.min = true;
    if (W.active === w) W.active = isPhone() ? null : W.topVisible(w);
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
    if (W.active === w) W.active = isPhone() ? null : W.topVisible(null);
    render();
  }
  function navigate(w, url) {
    w.url = W.clean(url);
    w.app = W.appOf(url);
    w.frame.src = url;
  }
  function closeStart() { if (startMenu && startMenu.open) startMenu.open = false; }

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
    var el = W.winTpl.content.firstElementChild.cloneNode(true);
    var task = W.taskTpl.content.firstElementChild.cloneNode(true);
    var w = { el: el, task: task, frame: el.querySelector("iframe"), url: W.clean(s.url), app: W.appOf(s.url), title: "…",
      x: s.x, y: s.y, w: s.w, h: s.h, min: !!s.min, max: !!s.max, live: null };
    if (!(w.w > 0 && w.h > 0)) { var p = W.firstPlace(); w.x = p.x; w.y = p.y; w.w = p.w; w.h = p.h; }
    W.opened++;
    var bar = el.querySelector(".w98-title");
    track(bar, w, function () { return { x: w.x, y: w.y }; }, function (from, dx, dy) { w.x = from.x + dx; w.y = from.y + dy; });
    track(el.querySelector(".w98-grip"), w, function () { return { w: w.w, h: w.h }; }, function (from, dx, dy) {
      var a = W.area();
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
      if (W.active === w && !w.min) minimise(w); else focus(w);
    });
    w.frame.addEventListener("load", function () { W.loaded(w); });
    wins.push(w);
    place(w);
    W.chrome(w);
    w.frame.src = s.url;
    desk.appendChild(el);
    W.tasks.appendChild(task);
    return w;
  }

  W.focus = focus;
  W.navigate = navigate;
  W.closeStart = closeStart;
  W.create = create;
})();
