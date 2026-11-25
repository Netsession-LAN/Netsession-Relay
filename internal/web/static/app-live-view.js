// Netsession Relay UI, live updates: puts one sample on the page (values, operating state, VLANs, tables).
(function () {
  "use strict";
  var N = window.NSR, doc = N.doc, $ = N.$, all = N.all, num = N.num, setText = N.setText, t = N.t, announce = N.announce;
  var el = N.el, prev = N.prev;
  var lastState = null;

  function get(o, path) {
    return path.split(".").reduce(function (x, k) { return x == null ? x : x[k]; }, o);
  }
  // re-render a table body from a list when it changed; columns: "field:classes,…"
  function rowsOf(tbody, list) {
    var key = JSON.stringify(list);
    if (prev[tbody.dataset.rows] === key) return;
    prev[tbody.dataset.rows] = key;
    var cols = tbody.dataset.cols.split(",").map(function (c) { var p = c.split(":"); return { f: p[0], cls: p[1] || "" }; });
    var frag = doc.createDocumentFragment();
    list.forEach(function (item) {
      var tr = el("tr");
      cols.forEach(function (c) {
        var v = item[c.f];
        var td = el("td", c.cls, /\bnum\b/.test(c.cls) ? num(v) : String(v));
        if (c.f === "names" && item.game) {
          var a = el("a", "rowlink", String(v));
          a.href = "/spiele/" + encodeURIComponent(item.game);
          td.textContent = "";
          td.appendChild(a);
          if (item.all) td.title = item.all;
        }
        tr.appendChild(td);
      });
      frag.appendChild(tr);
    });
    if (!list.length) {
      var td = el("td", "muted", tbody.dataset.empty || "–");
      td.colSpan = cols.length;
      frag.appendChild(el("tr")).appendChild(td);
    }
    tbody.replaceChildren(frag);
  }

  function apply(v) {
    liveValues(v);
    liveState(v);
    liveVLANs(v.vlans);
    var rule = N.ruleCounters(v.rules);
    if (N.games) N.liveGames(v.enabled, rule);
    N.liveGameSwitch(v.enabled);
    N.alarms(v.alarms);
    N.seen(v.seen);
    all("tbody[data-rows]").forEach(function (tb) { rowsOf(tb, v[tb.dataset.rows] || []); });
    N.liveDiagnosis(v, rule);
  }
  // plain values: text of every [data-live="path"]
  function liveValues(v) {
    all("[data-live]").forEach(function (e) {
      if (e.dataset.live === "games_on" && N.games) return; // counted from the rows
      var val = get(v, e.dataset.live);
      if (val == null) return;
      setText(e, typeof val === "number" ? num(val) : String(val));
    });
  }
  // operating state: tone, icon, what is offered
  function liveState(v) {
    var s = v.state;
    all("[data-tone-live]").forEach(function (e) { e.dataset.tone = s.tone; });
    all("[data-icon-live] use").forEach(function (u) {
      var href = "/static/icons.svg#i-" + s.icon;
      if (u.getAttribute("href") !== href) u.setAttribute("href", href);
    });
    all("[data-when]").forEach(function (e) { e.hidden = e.dataset.when.split(" ").indexOf(s.key) < 0; });
    N.stopLinks.forEach(function (l) { l.hidden = !s.can_stop; });
    N.resumeLinks.forEach(function (l) { l.hidden = s.key !== "paused"; });
    if ($("needs-apply")) $("needs-apply").hidden = !v.needs_apply;
    if (lastState && lastState !== s.key) announce(t("Betriebsart jetzt: %s. %s.", s.label, s.headline));
    lastState = s.key;
  }
  // VLAN tiles and rows; the bars are relative to the busiest VLAN (at least 10 frames/s)
  function liveVLANs(vlans) {
    var max = 10;
    vlans.forEach(function (x) { if (x.in_per_s > max) max = x.in_per_s; });
    vlans.forEach(function (x) {
      all('[data-vlan="' + x.vid + '"]').forEach(function (box) { liveVLAN(box, x, max); });
    });
  }
  function liveVLAN(box, x, max) {
    if (box.dataset.status !== x.status) box.dataset.status = x.status;
    setText(box.querySelector('[data-f="rate"]'), num(x.in_per_s));
    setText(box.querySelector('[data-f="label"]'), x.label);
    var link = box.querySelector("a.tile-link");
    if (link) {
      var href = x.alarm_kind ? "#alarm-" + x.alarm_kind : link.dataset.href;
      if (link.getAttribute("href") !== href) link.setAttribute("href", href);
    }
    setText(box.querySelector('[data-f="in"]'), num(x.in));
    setText(box.querySelector('[data-f="out"]'), num(x.out));
    var bar = box.querySelector(".tile-track i");
    if (bar) bar.style.width = Math.round(100 * x.in_per_s / max) + "%";
  }

  N.apply = apply;
})();
