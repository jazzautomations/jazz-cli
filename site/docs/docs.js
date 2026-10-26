/* docs.js: idioma, tema, menu no celular, busca nos títulos, copiar código e diagramas (Mermaid).
   Sem dependências além do Mermaid (carregado por <script> externo só nas páginas com diagrama). */
(function () {
  "use strict";
  var d = document.documentElement;
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };
  function store(k, v) { try { window.localStorage.setItem(k, v); } catch (e) {} }
  var body = document.body;
  function lang() { return d.getAttribute("data-lang") === "en" ? "en" : "pt"; }

  /* ---------- idioma ---------- */
  var langBtn = $("#lang-toggle"), searchIn = $("#doc-search");
  function setLang(l, save) {
    d.setAttribute("data-lang", l);
    d.setAttribute("lang", l === "pt" ? "pt-BR" : "en");
    document.title = body.getAttribute("data-title-" + l) || document.title;
    if (langBtn) langBtn.setAttribute("aria-label", l === "pt" ? "Switch to English" : "Mudar para português");
    if (searchIn) searchIn.setAttribute("placeholder", searchIn.getAttribute("data-ph-" + l) || "");
    if (save) store("jazz-lang", l);
    renderMermaid();
    if (searchIn && searchIn.value) runSearch();
  }
  if (langBtn) langBtn.addEventListener("click", function () { setLang(lang() === "pt" ? "en" : "pt", true); });

  /* ---------- tema ---------- */
  var themeBtn = $("#theme-toggle"), themeMeta = $('meta[name="theme-color"]');
  function theme() { return d.getAttribute("data-theme") === "light" ? "light" : "dark"; }
  function setTheme(t, save) {
    d.setAttribute("data-theme", t);
    if (themeBtn) themeBtn.setAttribute("aria-pressed", t === "light" ? "true" : "false");
    if (themeMeta) themeMeta.setAttribute("content", t === "light" ? "#f4f6f9" : "#242933");
    if (save) store("jazz-theme", t);
  }
  if (themeBtn) themeBtn.addEventListener("click", function () {
    setTheme(theme() === "light" ? "dark" : "light", true);
    $$("pre.mermaid[data-processed]").forEach(function (n) { n.textContent = n.getAttribute("data-src"); n.removeAttribute("data-processed"); });
    mermaidReady = false;
    renderMermaid();
  });

  /* ---------- menu (celular) ---------- */
  var sideBtn = $("#side-toggle"), sideBody = $("#side-body");
  if (sideBtn && sideBody) sideBtn.addEventListener("click", function () {
    var on = !sideBody.classList.contains("open");
    sideBody.classList.toggle("open", on);
    sideBtn.setAttribute("aria-expanded", on ? "true" : "false");
  });

  /* ---------- copiar código ---------- */
  $$("pre.code").forEach(function (pre) {
    var b = document.createElement("button");
    b.type = "button"; b.className = "copy"; b.textContent = "copiar";
    b.setAttribute("aria-label", "Copiar código / Copy code");
    b.addEventListener("click", function () {
      var txt = $("code", pre).textContent;
      if (!(navigator.clipboard && navigator.clipboard.writeText)) return;
      navigator.clipboard.writeText(txt).then(function () {
        b.textContent = lang() === "en" ? "copied" : "copiado"; b.classList.add("ok");
        setTimeout(function () { b.textContent = lang() === "en" ? "copy" : "copiar"; b.classList.remove("ok"); }, 1600);
      }, function () {});
    });
    pre.appendChild(b);
  });

  /* ---------- busca nos títulos ---------- */
  var index = null, loading = false, sel = -1;
  var resEl = $("#search-results"), statusEl = $("#search-status");
  function norm(s) { return s.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase(); }
  function load(cb) {
    if (index) return cb();
    if (loading) return;
    loading = true;
    fetch("/docs/search.json").then(function (r) { return r.json(); }).then(function (j) {
      index = j.map(function (e) { e.n = norm(e.t + " " + e.h); e.hn = norm(e.h || e.t); return e; });
      cb();
    }, function () { loading = false; });
  }
  function close() { if (resEl) { resEl.hidden = true; resEl.innerHTML = ""; } if (searchIn) searchIn.setAttribute("aria-expanded", "false"); sel = -1; }
  function runSearch() {
    var q = norm(searchIn.value).trim();
    if (!q) { close(); return; }
    load(function () {
      var toks = q.split(/\s+/), l = lang(), hits = [];
      index.forEach(function (e) {
        if (e.l !== "both" && e.l !== l) return;
        for (var i = 0; i < toks.length; i++) if (e.n.indexOf(toks[i]) < 0) return;
        var s = (e.hn === q ? 0 : e.hn.indexOf(q) === 0 ? 1 : e.hn.indexOf(q) > 0 ? 2 : 3) + (e.h ? 0 : -0.5);
        hits.push([s, e]);
      });
      hits.sort(function (a, b) { return a[0] - b[0]; });
      hits = hits.slice(0, 12);
      resEl.innerHTML = "";
      if (!hits.length) {
        var li = document.createElement("li"); li.className = "none";
        li.textContent = l === "en" ? "No results" : "Nada encontrado";
        resEl.appendChild(li);
      }
      hits.forEach(function (h) {
        var e = h[1], li = document.createElement("li"), a = document.createElement("a");
        a.href = e.u; a.textContent = e.h || e.t;
        var sm = document.createElement("small"); sm.textContent = e.h ? e.t : (l === "en" ? "page" : "página");
        a.appendChild(sm); li.appendChild(a); resEl.appendChild(li);
      });
      resEl.hidden = false; searchIn.setAttribute("aria-expanded", "true");
      statusEl.textContent = hits.length + (l === "en" ? " results" : " resultados");
      sel = -1;
    });
  }
  function move(delta) {
    var as = $$("a", resEl); if (!as.length) return;
    if (sel >= 0) as[sel].classList.remove("sel");
    sel = (sel + delta + as.length) % as.length;
    as[sel].classList.add("sel"); as[sel].scrollIntoView({ block: "nearest" });
  }
  if (searchIn && resEl) {
    searchIn.addEventListener("input", runSearch);
    searchIn.addEventListener("focus", function () { load(function () {}); });
    searchIn.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown") { e.preventDefault(); move(1); }
      else if (e.key === "ArrowUp") { e.preventDefault(); move(-1); }
      else if (e.key === "Enter") {
        var as = $$("a", resEl), t = as[sel >= 0 ? sel : 0];
        if (t) { e.preventDefault(); window.location.href = t.getAttribute("href"); }
      } else if (e.key === "Escape") { close(); searchIn.blur(); }
    });
    document.addEventListener("click", function (e) { if (!e.target.closest(".search")) close(); });
    resEl.addEventListener("click", function () { close(); });
    document.addEventListener("keydown", function (e) {
      if (e.key === "/" && !/^(INPUT|TEXTAREA)$/.test(document.activeElement.tagName)) {
        e.preventDefault();
        if (sideBody && !sideBody.classList.contains("open") && getComputedStyle(sideBody).display === "none") sideBtn.click();
        searchIn.focus();
      }
    });
  }

  /* ---------- diagramas ---------- */
  var mermaidReady = false;
  function renderMermaid() {
    if (!window.mermaid) return;
    if (!mermaidReady) {
      window.mermaid.initialize({
        startOnLoad: false, securityLevel: "strict",
        flowchart: { useMaxWidth: false }, sequence: { useMaxWidth: false }, state: { useMaxWidth: false },
        theme: theme() === "light" ? "neutral" : "dark",
        fontFamily: "IBM Plex Sans, system-ui, sans-serif"
      });
      mermaidReady = true;
    }
    // só os visíveis (o outro idioma está display:none e o Mermaid não consegue medir)
    var nodes = $$("pre.mermaid:not([data-processed])").filter(function (n) { return n.offsetParent !== null; });
    if (nodes.length) window.mermaid.run({ nodes: nodes }).catch(function () {});
  }

  setLang(lang(), false);
  setTheme(theme(), false);
  if (window.mermaid) renderMermaid(); else window.addEventListener("load", renderMermaid);
})();
