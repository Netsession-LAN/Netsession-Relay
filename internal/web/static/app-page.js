// Netsession Relay UI: the page around the content: menus, pop-ups, links to a tile or an alarm, the navigation.
(function () {
  "use strict";
  var N = window.NSR, doc = N.doc, $ = N.$, all = N.all, announce = N.announce;
  // account menu and the Start menu of the theme "98": close on outside click and Esc
  all("details.account, details.w98-start").forEach(function (menu) {
    doc.addEventListener("click", function (e) {
      if (menu.open && (!menu.contains(e.target) || e.target.closest(".w98-menu a"))) menu.open = false;
    });
    doc.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && menu.open) { menu.open = false; menu.querySelector("summary").focus(); }
    });
    menu.addEventListener("toggle", function () {
      if (!menu.open) all("details[open]", menu).forEach(function (d) { d.open = false; });
    });
  });
  // theme "98": a maximised window stays maximised on the next page
  var winMax = $("w98-max");
  if (winMax) {
    try { winMax.checked = localStorage.getItem("w98max") === "1"; } catch (e) { /* storage blocked */ }
    winMax.addEventListener("change", function () {
      try { localStorage.setItem("w98max", winMax.checked ? "1" : "0"); } catch (e) { /* storage blocked */ }
    });
  }

  // "+2" pop-ups: one open at a time, close on outside click and Esc
  doc.addEventListener("click", function (e) {
    all("details.more[open]").forEach(function (m) { if (!m.contains(e.target)) m.open = false; });
  });
  doc.addEventListener("keydown", function (e) {
    if (e.key === "Tab") doc.documentElement.classList.add("kbd"); // focus rings on the login form only after the keyboard was used
    if (e.key !== "Escape") return;
    all("details.more[open]").forEach(function (m) { m.open = false; if (m.contains(doc.activeElement)) m.querySelector("summary").focus(); });
  });

  // links to a tile or an alarm card on the same page: open a folded parent, move focus there, say what it is
  function goTarget(id, say) {
    var target = $(id);
    if (!target) return;
    for (var n = target.parentNode; n && n !== doc; n = n.parentNode) if (n.tagName === "DETAILS") n.open = true;
    if (target.hasAttribute("tabindex")) target.focus({ preventScroll: true });
    target.scrollIntoView({ block: "center" });
    if (say) {
      var name = target.querySelector(".alarm-title b, .tile-name");
      if (name) announce(name.textContent.trim());
    }
  }
  doc.addEventListener("click", function (e) {
    var a = e.target.closest ? e.target.closest('a[href^="#netz-"], a[href^="#alarm-"]') : null;
    if (!a) return;
    var id = a.getAttribute("href").slice(1);
    if (!$(id)) return;
    e.preventDefault();
    history.replaceState(null, "", "#" + id);
    // :target only follows real navigation; mark the target ourselves
    all(".is-target").forEach(function (x) { x.classList.remove("is-target"); });
    $(id).classList.add("is-target");
    goTarget(id, true);
  });
  if (location.hash.length > 1 && /^#[\w-]+$/.test(location.hash)) setTimeout(function () { goTarget(location.hash.slice(1)); }, 0);

  // the navigation scrolls sideways on phones: bring the current entry into view
  var navNow = doc.querySelector('.nav a[aria-current="page"]');
  if (navNow && navNow.scrollIntoView && window.innerWidth < 1024) navNow.scrollIntoView({ inline: "center", block: "nearest" });
})();
