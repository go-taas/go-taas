/* Go TaaS site — language switching, persistence, and small UI behaviors */
(function (global) {
  "use strict";

  var STORAGE_KEY = "go-taas-site-lang";
  var SUPPORTED = ["en", "zh-CN"];
  var DEFAULT_LANG = "en";
  var dict = global.SITE_I18N || {};

  var state = {
    lang: DEFAULT_LANG
  };

  var toggleBtn = document.getElementById("lang-toggle");

  function normalizeLang(raw) {
    if (!raw) return null;
    var lower = String(raw).toLowerCase();
    if (lower.indexOf("zh") === 0) return "zh-CN";
    if (lower.indexOf("en") === 0) return "en";
    return null;
  }

  function langFromHash() {
    // location.hash includes the leading "#" — strip it before matching.
    // Hash parameters are separated by "," / ";" on the landing page and by
    // "&" on the documentation browser, which carries several parameters.
    var hash = String(global.location.hash || "").replace(/^#/, "");
    var match = /(?:^|[,;&])lang=(zh-CN|en)(?:$|[,;&])/.exec(hash);
    return match ? match[1] : null;
  }

  function detectLang() {
    // 1. URL hash: #lang=zh-CN (shareable links)
    var fromHash = langFromHash();
    if (fromHash) return fromHash;

    // 2. Saved preference
    try {
      var saved = global.localStorage.getItem(STORAGE_KEY);
      var normalized = normalizeLang(saved);
      if (normalized) return normalized;
    } catch (e) {
      /* localStorage unavailable (private mode etc.) — fall through */
    }

    // 3. Browser preference
    var candidates = (global.navigator.languages || []).concat([global.navigator.language || ""]);
    for (var i = 0; i < candidates.length; i++) {
      var lang = normalizeLang(candidates[i]);
      if (lang) return lang;
    }

    return DEFAULT_LANG;
  }

  function applyLang(lang) {
    state.lang = lang;
    var table = dict[lang] || {};

    var nodes = document.querySelectorAll("[data-i18n]");
    for (var i = 0; i < nodes.length; i++) {
      var node = nodes[i];
      var key = node.getAttribute("data-i18n");
      var value = table[key];
      if (typeof value === "string") {
        node.textContent = value;
      }
    }

    // Nodes that hold markup (they set their own content) and inputs that
    // carry placeholder copy are handled here so pages stay declarative.
    var htmlNodes = document.querySelectorAll("[data-i18n-html]");
    for (var h = 0; h < htmlNodes.length; h++) {
      var htmlNode = htmlNodes[h];
      var htmlValue = table[htmlNode.getAttribute("data-i18n-html")];
      if (typeof htmlValue === "string") {
        htmlNode.innerHTML = htmlValue;
      }
    }

    var placeholders = document.querySelectorAll("[data-i18n-placeholder]");
    for (var p = 0; p < placeholders.length; p++) {
      var field = placeholders[p];
      var placeholder = table[field.getAttribute("data-i18n-placeholder")];
      if (typeof placeholder === "string") {
        field.setAttribute("placeholder", placeholder);
      }
    }

    document.documentElement.setAttribute("lang", lang);
    if (toggleBtn) {
      toggleBtn.textContent = lang === "zh-CN" ? "English" : "中文";
      toggleBtn.setAttribute("aria-label", lang === "zh-CN" ? "Switch to English" : "切换到中文");
    }

    try {
      global.localStorage.setItem(STORAGE_KEY, lang);
    } catch (e) {
      /* ignore storage failures */
    }

    // Other scripts (for example the documentation browser) re-render their
    // own content when the language changes.
    document.dispatchEvent(
      new CustomEvent("go-taas:lang", { detail: { lang: lang, dict: table } })
    );
  }

  function toggleLang() {
    applyLang(state.lang === "zh-CN" ? "en" : "zh-CN");
  }

  function init() {
    applyLang(detectLang());

    if (toggleBtn) {
      toggleBtn.addEventListener("click", toggleLang);
    }

    // Keep the language in sync when the URL hash changes (e.g. back navigation).
    global.addEventListener("hashchange", function () {
      var fromHash = langFromHash();
      if (fromHash && fromHash !== state.lang) {
        applyLang(fromHash);
      }
    });
  }

  // Pages that need to switch the language programmatically (the documentation
  // browser swaps to the matching document variant) use this small surface
  // instead of poking at the toggle button.
  global.SITE_LANG = {
    get: function () {
      return state.lang;
    },
    set: applyLang,
    supported: SUPPORTED.slice()
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})(window);
