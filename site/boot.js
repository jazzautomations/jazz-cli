/* boot.js: roda antes da pintura (síncrono, minúsculo) para não piscar idioma/tema. */
(function () {
  var d = document.documentElement, lang = null, theme = null;
  function get(k) { try { return window.localStorage.getItem(k); } catch (e) { return null; } }
  try {
    var q = new URLSearchParams(window.location.search).get("lang");
    if (q === "pt" || q === "en") lang = q;
  } catch (e) {}
  if (!lang) { var s = get("jazz-lang"); if (s === "pt" || s === "en") lang = s; }
  if (!lang) {
    var nav = (navigator.languages && navigator.languages[0]) || navigator.language || "pt";
    lang = /^pt\b/i.test(nav) ? "pt" : "en";
  }
  var t = get("jazz-theme");
  try {
    var qt = new URLSearchParams(window.location.search).get("theme");
    if (qt === "light" || qt === "dark") t = qt;
  } catch (e) {}
  theme = t === "light" || t === "dark" ? t : "dark";
  d.setAttribute("data-lang", lang);
  d.setAttribute("lang", lang === "pt" ? "pt-BR" : "en");
  d.setAttribute("data-theme", theme);
  d.className = d.className.replace(/\bno-js\b/, "js");
})();
