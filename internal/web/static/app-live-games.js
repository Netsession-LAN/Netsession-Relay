// Netsession Relay UI, live updates: rule and game counters, switches someone else used, the diagnosis.
(function () {
  "use strict";
  var N = window.NSR, doc = N.doc, $ = N.$, all = N.all, num = N.num, setText = N.setText, t = N.t, prev = N.prev;
  // rule counters (game page); returns the rules by ID
  function ruleCounters(rules) {
    var rule = {};
    rules.forEach(function (r) { rule[r.id] = r; });
    all("[data-rule]").forEach(function (tr) {
      var r = rule[tr.dataset.rule] || { rx: 0, relayed: 0 };
      setText(tr.querySelector('[data-f="rx"]'), num(r.rx));
      setText(tr.querySelector('[data-f="relayed"]'), num(r.relayed));
    });
    return rule;
  }
  function sumRX(ids, rule) {
    var sum = 0;
    ids.split(" ").forEach(function (id) { if (rule[id]) sum += rule[id].rx; });
    return sum;
  }
  // per-game counters (games list) and games someone else switched
  function liveGames(enabledIDs, rule) {
    var enabled = {}, changed = false;
    enabledIDs.forEach(function (id) { enabled[id] = true; });
    all(".grow[data-rules]", N.games).forEach(function (row) {
      var sum = sumRX(row.dataset.rules, rule), before = prev["g:" + row.dataset.id];
      row.classList.toggle("hot", before !== undefined && sum > before);
      prev["g:" + row.dataset.id] = sum;
      setText(row.querySelector('[data-f="rx"]'), num(sum));
      if (followSwitch(row, !!enabled[row.dataset.id])) changed = true;
    });
    if (changed) doc.dispatchEvent(new Event("relay:changed"));
  }
  // someone else switched the game of a row: follow, unless this switch is just being used. True if it changed.
  function followSwitch(row, on) {
    var f = row.querySelector("form.toggle-form");
    if ((row.dataset.on === "1") === on || (f && f.querySelector(".busy"))) return false;
    if (f) N.setSwitch(f, on);
    else { row.dataset.on = on ? "1" : "0"; setText(row.querySelector("[data-onoff]"), on ? t("An") : t("Aus")); }
    return true;
  }
  // the switch of a single game's page
  function liveGameSwitch(enabledIDs) {
    var single = doc.querySelector("[data-game-switch]");
    if (!single) return;
    var isOn = enabledIDs.indexOf(single.dataset.id) >= 0, sf = single.querySelector("form.toggle-form");
    if (!sf) { setText(single.querySelector("[data-onoff]"), isOn ? t("An") : t("Aus")); return; }
    var shown = sf.querySelector(".switch").getAttribute("aria-checked") === "true";
    if (!sf.querySelector(".busy") && shown !== isOn) N.setSwitch(sf, isOn);
  }
  // diagnosis: fetch the server-rendered fragment again when something relevant flipped
  function liveDiagnosis(v, rule) {
    var diag = $("diagnose");
    if (!diag) return;
    var rx = 0, tx = 0;
    diag.dataset.rules.split(" ").forEach(function (id) { if (rule[id]) { rx += rule[id].rx; tx += rule[id].relayed; } });
    var sig = [v.state.key, v.enabled.indexOf(diag.dataset.id) >= 0, rx > 0, tx > 0, v.hash].join("|");
    if (prev.diag !== undefined && prev.diag !== sig) {
      fetch(diag.dataset.fragment, { credentials: "same-origin" }).then(function (r) { return r.ok ? r.text() : null; }).then(function (html) {
        if (html) diag.innerHTML = html; // same-origin, rendered and escaped by the server template
      }).catch(function () {});
    }
    prev.diag = sig;
  }

  N.ruleCounters = ruleCounters;
  N.liveGames = liveGames;
  N.liveGameSwitch = liveGameSwitch;
  N.liveDiagnosis = liveDiagnosis;
})();
