// Netsession Relay UI, live updates: the server-sent events connection, its watchdog and the session end.
(function () {
  "use strict";
  var N = window.NSR, doc = N.doc, body = N.body, $ = N.$, setText = N.setText, t = N.t, fmtTime = N.fmtTime, apply = N.apply;
  var liveURL = body.dataset.liveUrl;
  if (!liveURL || !window.EventSource) return;
  var liveEl = $("live"), liveText = $("live-text"), conn = $("conn");
  var es = null, lastAt = Date.now(), lastData = null, lost = false;

  function setLive(state) {
    if (!liveEl) return;
    liveEl.dataset.state = state;
    liveEl.removeAttribute("title");
    setText(liveText, state === "on" ? fmtTime(new Date(lastAt), "time") : state === "off" ? t("getrennt") : t("verbindet"));
    liveEl.title = state === "on" ? t("Live: Stand von %s", fmtTime(new Date(lastAt), "time")) : "";
  }
  function markLost(isLost) {
    if (lost === isLost) return;
    lost = isLost;
    body.classList.toggle("stale", lost);
    if (conn) {
      if (lost) setText($("conn-text"), t("Keine Verbindung zum Relay-Server. Die Zahlen sind von %s. Neuer Versuch läuft.", lastAt ? fmtTime(new Date(lastAt), "time") : t("vorhin")));
      conn.hidden = !lost;
    }
    setLive(lost ? "off" : "on");
  }
  function onStatus(e) {
    var v;
    try { v = JSON.parse(e.data); } catch (err) { return; }
    lastAt = Date.now();
    lastData = v;
    markLost(false);
    setLive("on");
    apply(v);
  }
  function connect() {
    if (es) es.close();
    es = new EventSource(liveURL);
    es.addEventListener("status", onStatus);
    es.addEventListener("bye", sessionGone);
    es.onerror = function () {
      // EventSource retries by itself while CONNECTING; CLOSED means the server refused (e.g. session ended)
      if (es.readyState === 2) checkSession();
    };
  }
  function sessionGone() {
    if (es) es.close();
    es = null;
    body.classList.add("stale");
    setLive("off");
    if (conn) {
      setText($("conn-text"), t("Deine Sitzung ist abgelaufen. Melde dich neu an."));
      var a = conn.querySelector("a");
      a.textContent = t("Anmelden");
      a.href = "/login";
      if (framed) a.target = "_top";
      conn.hidden = false;
    }
    clearInterval(watch);
  }
  function checkSession() {
    fetch("/api/v1/status", { headers: { "Accept": "application/json" }, credentials: "same-origin" }).then(function (r) {
      if (r.status === 401) sessionGone(); else setTimeout(connect, 3000);
    }).catch(function () { setTimeout(connect, 3000); });
  }
  // watchdog: no sample for 7 s means the numbers are stale, whatever the socket says
  var watch = setInterval(function () {
    if (doc.hidden || !es) return;
    if (lastAt && Date.now() - lastAt > 7000) markLost(true);
    if (lastAt && Date.now() - lastAt > 20000 && es.readyState !== 0) connect();
  }, 1000);
  // a hidden tab gives its connection back (browsers allow six per server) and reconnects when shown
  function pause() { if (es) { es.close(); es = null; } }
  function resume() { if (!es) { setLive("wait"); lastAt = Date.now(); connect(); } }
  doc.addEventListener("visibilitychange", function () {
    if (doc.hidden) pause(); else if (!parked) resume();
  });
  // theme "98": the page sits in a window of the desktop (w98.js). The desktop says when the window can be seen;
  // a minimised or covered window gives its connection back, too, and its numbers go grey.
  var framed = window.parent !== window, parked = framed;
  if (framed) {
    window.addEventListener("message", function (e) {
      if (e.origin !== location.origin || !e.data || e.data.w98 !== "live") return;
      parked = !e.data.on;
      body.classList.toggle("stale", parked || lost);
      if (parked) pause(); else if (!doc.hidden) resume();
    });
    window.parent.postMessage({ w98: "ready" }, location.origin);
  }
  setLive("wait");
  if (!parked) connect();
})();
