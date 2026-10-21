/* app.js: idioma, tema, abas de instalação, copiar e o terminal que "digita".
   Sem dependências. Tudo funciona sem JS (conteúdo estático); isto só melhora. */
(function () {
  "use strict";
  var d = document.documentElement;
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };
  function store(k, v) { try { window.localStorage.setItem(k, v); } catch (e) {} }

  /* ---------- idioma ---------- */
  var TITLES = {
    pt: "jazz-cli · agente de código no terminal",
    en: "jazz-cli · a coding agent for the terminal"
  };
  var langBtn = $("#lang-toggle");
  function setLang(l, save) {
    d.setAttribute("data-lang", l);
    d.setAttribute("lang", l === "pt" ? "pt-BR" : "en");
    document.title = TITLES[l];
    if (langBtn) langBtn.setAttribute("aria-label", l === "pt" ? "Switch to English" : "Mudar para português");
    if (save) store("jazz-lang", l);
  }
  setLang(d.getAttribute("data-lang") === "en" ? "en" : "pt", false);
  if (langBtn) langBtn.addEventListener("click", function () {
    setLang(d.getAttribute("data-lang") === "pt" ? "en" : "pt", true);
  });

  /* ---------- tema ---------- */
  var themeBtn = $("#theme-toggle");
  var themeMeta = $('meta[name="theme-color"]');
  function setTheme(t, save) {
    d.setAttribute("data-theme", t);
    if (themeBtn) themeBtn.setAttribute("aria-pressed", t === "light" ? "true" : "false");
    if (themeMeta) themeMeta.setAttribute("content", t === "light" ? "#f4f6f9" : "#242933");
    if (save) store("jazz-theme", t);
  }
  setTheme(d.getAttribute("data-theme") === "light" ? "light" : "dark", false);
  if (themeBtn) themeBtn.addEventListener("click", function () {
    setTheme(d.getAttribute("data-theme") === "light" ? "dark" : "light", true);
  });

  /* ---------- abas (padrão ARIA tabs) ---------- */
  var tabs = $$('[role="tab"]');
  function selectTab(tab, focus) {
    tabs.forEach(function (t) {
      var on = t === tab;
      t.setAttribute("aria-selected", on ? "true" : "false");
      t.tabIndex = on ? 0 : -1;
      var panel = document.getElementById(t.getAttribute("aria-controls"));
      if (panel) panel.hidden = !on;
    });
    if (focus) tab.focus();
  }
  if (tabs.length) {
    selectTab(tabs[0], false);
    tabs.forEach(function (t, i) {
      t.addEventListener("click", function () { selectTab(t, false); });
      t.addEventListener("keydown", function (e) {
        var n = null;
        if (e.key === "ArrowRight") n = tabs[(i + 1) % tabs.length];
        else if (e.key === "ArrowLeft") n = tabs[(i - 1 + tabs.length) % tabs.length];
        else if (e.key === "Home") n = tabs[0];
        else if (e.key === "End") n = tabs[tabs.length - 1];
        if (n) { e.preventDefault(); selectTab(n, true); }
      });
    });
  }

  /* ---------- copiar ---------- */
  $$(".copy").forEach(function (b) {
    b.addEventListener("click", function () {
      var text = b.getAttribute("data-copy") || "";
      var done = function () {
        b.classList.add("ok");
        var spans = $$("span", b), old = spans.map(function (s) { return s.textContent; });
        spans.forEach(function (s) { s.textContent = s.getAttribute("data-l") === "en" ? "copied" : "copiado"; });
        setTimeout(function () {
          b.classList.remove("ok");
          spans.forEach(function (s, k) { s.textContent = old[k]; });
        }, 1600);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function () {});
      }
    });
  });

  /* ---------- terminal ---------- */
  var term = $("#term"), body = $("#term-body");
  if (!term || !body) return;
  var reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (reduce) return; // fica a sessão inteira, estática

  var lines = $$(".tl", body);
  var status = $("#t-status"), spinEl = $("#t-spin"), labelEl = $("#t-label"),
      elEl = $("#t-el"), metaEl = $("#t-meta"), phaseEl = $("#t-phase");
  var replay = $("#term-replay");
  var FRAMES = ["·", "✢", "✶", "✻", "✽", "✻", "✶", "✢"];
  var run = 0;

  function sleep(ms, id) {
    return new Promise(function (res, rej) {
      setTimeout(function () { id === run ? res() : rej("cancel"); }, ms);
    });
  }

  function spin(label, ms, id) {
    return new Promise(function (res, rej) {
      var t0 = performance.now(), f = 0;
      var model = label !== "rodando";
      labelEl.textContent = label;
      metaEl.hidden = !model;
      status.classList.remove("idle");
      var timer = setInterval(function () {
        if (id !== run) { clearInterval(timer); rej("cancel"); return; }
        var el = performance.now() - t0;
        spinEl.textContent = FRAMES[f++ % FRAMES.length];
        elEl.textContent = Math.round(el / 1000) + "s";
        if (model) phaseEl.textContent = el < ms * 0.55 ? "aguardando 1º token" : "gerando";
        if (el >= ms) { clearInterval(timer); status.classList.add("idle"); res(); }
      }, 125);
    });
  }

  function type(line, id) {
    var tx = $(".tx", line);
    if (!tx) return Promise.resolve();
    var full = tx.getAttribute("data-full") || tx.textContent;
    tx.setAttribute("data-full", full);
    tx.textContent = "";
    line.classList.add("typing");
    var i = 0;
    return new Promise(function (res, rej) {
      (function step() {
        if (id !== run) { rej("cancel"); return; }
        i++;
        tx.textContent = full.slice(0, i);
        if (i >= full.length) { line.classList.remove("typing"); res(); return; }
        setTimeout(step, 28 + Math.random() * 45);
      })();
    });
  }

  function reset() {
    lines.forEach(function (l) {
      var tx = $(".tx", l);
      if (tx && tx.getAttribute("data-full")) tx.textContent = tx.getAttribute("data-full");
      l.classList.remove("typing");
      if (l.hasAttribute("data-w")) l.classList.add("pending");
    });
    status.classList.add("idle");
    replay.hidden = true;
  }

  function play() {
    var id = ++run;
    term.classList.add("anim");
    reset();
    var chain = Promise.resolve();
    lines.forEach(function (l) {
      if (!l.hasAttribute("data-w")) return;
      chain = chain.then(function () {
        var w = parseInt(l.getAttribute("data-w"), 10) || 0;
        var s = l.getAttribute("data-s");
        return s ? spin(s, w, id) : sleep(w, id);
      }).then(function () {
        l.classList.remove("pending");
        if (l.hasAttribute("data-type")) return type(l, id);
      });
    });
    chain.then(function () { if (id === run) replay.hidden = false; }, function () {});
  }

  replay.addEventListener("click", play);

  if ("IntersectionObserver" in window) {
    // começa quando o terminal aparece; as linhas ficam reservadas (sem salto de layout)
    term.classList.add("anim");
    reset();
    var io = new IntersectionObserver(function (es) {
      if (es.some(function (e) { return e.isIntersecting; })) { io.disconnect(); play(); }
    }, { threshold: 0.2 });
    io.observe(term);
  } else {
    play();
  }
})();
