// Netsession Relay UI: games list: view (on | off | all) + search. The search always looks through all games.
(function () {
  "use strict";
  var N = window.NSR, doc = N.doc, $ = N.$, all = N.all, setText = N.setText, t = N.t, dlg = N.dlg;
  var games = $("games"), applyFilter = function () {};
  if (games) {
    var search = $("game-search"), empty = $("game-empty"), none = $("game-none"), viewBox = $("game-view");
    var rows = all(".grow", games);
    var view = viewBox.dataset.view || "on";
    applyFilter = function (quiet) {
      var q = (search.value || "").trim().toLowerCase();
      var shown = 0;
      rows.forEach(function (row) {
        var d = row.dataset, ok;
        if (q) ok = d.search.toLowerCase().indexOf(q) >= 0;
        else if (d.nolan) ok = view !== "on";
        else ok = view === "all" || (view === "on") === (d.on === "1");
        row.hidden = !ok;
        if (ok) shown++;
      });
      all("[data-gsec]", games).forEach(function (sec) {
        var n = all(".grow:not([hidden])", sec).length;
        sec.hidden = !n;
        var c = sec.querySelector("[data-count]");
        if (c) setText(c, String(n));
        if (sec.tagName === "DETAILS" && q && n) sec.open = true;
      });
      empty.hidden = shown > 0 || !q;
      if (none) none.hidden = shown > 0 || !!q;
      if (!quiet) setText($("game-count"), shown === 1 ? t("1 Spiel gefunden") : t("%s Spiele gefunden", shown));
    };
    var setView = function (name) {
      view = name;
      all(".seg-item", viewBox).forEach(function (o) {
        if (o.dataset.view === name) o.setAttribute("aria-current", "true"); else o.removeAttribute("aria-current");
      });
      applyFilter();
    };
    search.addEventListener("input", function () { applyFilter(); });
    all(".seg-item", viewBox).forEach(function (c) {
      c.addEventListener("click", function (e) {
        e.preventDefault();
        history.replaceState(null, "", c.getAttribute("href"));
        setView(c.dataset.view);
      });
    });
    doc.addEventListener("keydown", function (e) {
      if (e.key === "/" && !/^(INPUT|TEXTAREA|SELECT)$/.test(doc.activeElement.tagName) && !(dlg && dlg.open)) { e.preventDefault(); search.focus(); search.select(); }
      if (e.key === "Escape" && doc.activeElement === search && search.value) { search.value = ""; applyFilter(); }
    });
    var params = new URLSearchParams(location.search);
    if (params.get("q")) search.value = params.get("q");
    applyFilter(true);
    // a game switched here keeps its place until the view is chosen again: nothing jumps away under the pointer
    doc.addEventListener("relay:changed", function () { countGames(); });
  }
  function countGames() {
    if (!games) return;
    var on = all('.grow[data-on="1"]', games).length, off = all('.grow[data-on="0"]', games).length;
    setText($("n-on"), String(on));
    setText($("n-off"), String(off));
    all('[data-live="games_on"]').forEach(function (el) { setText(el, String(on)); });
  }

  N.games = games;
})();
