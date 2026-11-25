// Netsession Relay UI, live updates: the alarm cards, the alarms already seen and the alarm tile.
(function () {
  "use strict";
  var N = window.NSR, doc = N.doc, $ = N.$, num = N.num, setText = N.setText, t = N.t, announce = N.announce;
  var fmtTime = N.fmtTime, icon = N.icon, el = N.el, prev = N.prev;
  var seenAlarms = null;

  function netsLine(a) {
    var p = el("p", "alarm-nets");
    p.appendChild(el("span", "label", (a.nets.length === 1 ? t("Betrifft 1 Netz") : t("Betrifft %s Netze", a.nets.length))));
    a.nets.forEach(function (n) {
      var link = el("a", "netchip");
      link.href = "#netz-" + n.vid;
      var key = el("span", "key key-sm", n.key);
      key.setAttribute("aria-hidden", "true");
      link.appendChild(key);
      link.appendChild(doc.createTextNode(n.name));
      p.appendChild(link);
    });
    return p;
  }
  function alarmCard(a, open, canAck) {
    var li = el("li", "alarm note"), box = el("div", "note-body"), title = el("p", "alarm-title");
    li.dataset.tone = open ? "danger" : "neutral";
    li.id = "alarm-" + a.kind;
    li.tabIndex = -1;
    li.appendChild(icon(open ? "alert" : "check"));
    title.appendChild(el("b", "", a.title));
    title.appendChild(doc.createTextNode(" "));
    title.appendChild(el("span", "alarm-meta", t("%s× · zuletzt %s", num(a.count), fmtTime(new Date(a.last), "time")) + (open && a.quiet ? t(" · seitdem ruhig") : "") + (!open && a.acked_by ? t(" · gesehen von %s", a.acked_by) : "")));
    box.appendChild(title);
    if (open && a.hint) box.appendChild(el("p", "", a.hint));
    if (a.nets && a.nets.length) box.appendChild(netsLine(a));
    if (a.where) {
      var w = el("p", "small muted", "Betrifft: ");
      w.appendChild(el("span", "mono", a.where));
      box.appendChild(w);
    }
    if (open && a.detail) {
      var det = el("details", "fold"), sum = el("summary");
      sum.appendChild(el("span", "label", t("Technische Details")));
      det.appendChild(sum);
      det.appendChild(el("p", "small muted", a.detail));
      det.dataset.key = "detail-" + a.kind;
      box.appendChild(det);
    }
    if (open && canAck) box.appendChild(ackForm(a.kind));
    li.appendChild(box);
    return li;
  }
  function alarmNets(list) {
    var seenNets = {}, n = 0;
    list.forEach(function (a) { (a.nets || []).forEach(function (x) { if (!seenNets[x.vid]) { seenNets[x.vid] = 1; n++; } }); });
    return n === 0 ? t("siehe unten") : n === 1 ? t("betrifft 1 Netz") : t("betrifft %s Netze", n);
  }
  function alarms(list) {
    var ul = $("alarms");
    var kinds = list.map(function (a) { return a.kind; });
    if (seenAlarms) {
      var fresh = list.filter(function (a) { return seenAlarms.indexOf(a.kind) < 0; });
      if (fresh.length) announce(t("Neuer Alarm: ") + fresh.map(function (a) { return a.title; }).join(", "));
      else if (seenAlarms.length && !kinds.length) announce(t("Alarme beendet."));
    }
    seenAlarms = kinds;
    var st = $("stat-alarm");
    if (st) {
      st.classList.toggle("stat-alarm", list.length > 0);
      setText($("stat-alarm-sub"), list.length ? alarmNets(list) : t("alles ruhig"));
    }
    if (!ul) return;
    // count and time change with every sample; rebuild only when something else changed, so an opened
    // "Technische Details" and the keyboard focus stay where they are
    var key = JSON.stringify(list.map(function (a) { return [a.kind, a.title, a.hint, a.nets, a.where, a.detail]; }));
    if (prev.alarms === key) {
      list.forEach(function (a) {
        var meta = doc.querySelector("#alarm-" + a.kind + " .alarm-meta");
        setText(meta, t("%s× · zuletzt %s", num(a.count), fmtTime(new Date(a.last), "time")) + (a.quiet ? t(" · seitdem ruhig") : ""));
      });
      return;
    }
    prev.alarms = key;
    var frag = doc.createDocumentFragment();
    list.forEach(function (a) { frag.appendChild(alarmCard(a, true, ul.hasAttribute("data-can-ack"))); });
    ul.replaceChildren(frag);
    ul.hidden = !list.length;
    $("alarms-empty").hidden = list.length > 0;
  }
  function ackForm(kind) {
    var f = el("form", "ack-form");
    f.method = "post";
    f.action = "/alarme/" + encodeURIComponent(kind) + "/gesehen";
    var c = el("input"); c.type = "hidden"; c.name = "csrf"; c.value = N.csrf; f.appendChild(c);
    var b = el("button", "btn-outline btn-sm"); b.type = "submit";
    b.appendChild(icon("check")); b.appendChild(el("span", "", t("Gesehen")));
    f.appendChild(b);
    return f;
  }
  function seen(list) {
    var box = $("seen-box"), ul = $("alarms-seen");
    if (!box || !ul) return;
    list = list || [];
    var key = JSON.stringify(list);
    if (prev.seen === key) return;
    prev.seen = key;
    box.hidden = !list.length;
    setText($("seen-count"), String(list.length));
    var frag = doc.createDocumentFragment();
    list.forEach(function (a) { frag.appendChild(alarmCard(a, false, false)); });
    ul.replaceChildren(frag);
  }

  N.alarms = alarms;
  N.seen = seen;
})();
