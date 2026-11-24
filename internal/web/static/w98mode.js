// Netsession Relay, theme "98": loaded in the head of a page the server sent as a single window because the
// browser did not say what the request was for (no Sec-Fetch-Dest over plain http, see theme.go w98Mode).
// Runs before the first paint: inside a window of the desktop the page hides its own chrome ("frame"),
// at the top it hides its single window, and w98.js builds the desktop ("shell"). Rules in theme98.css.
(function () {
  "use strict";
  var root = document.documentElement;
  if (!root.dataset.w98) root.dataset.w98 = window.parent !== window ? "frame" : "shell";
})();
