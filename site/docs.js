/* Go TaaS site — documentation browser (site/docs.html).
 *
 * Renders the bilingual document set from site/docs/ in the browser:
 *   - site/docs/manifest.json indexes every document (titles, heading outline,
 *     metadata); bodies are fetched on demand from site/docs/**.md.
 *   - marked renders the Markdown, highlight.js colors the code, and Mermaid
 *     (loaded lazily, only for documents that contain a diagram) draws it.
 *
 * Routing keeps every view shareable and deep-linkable. The URL carries the
 * document id, the UI language, an optional document language (used when a
 * reader pins one document to the other language) and an anchor:
 *
 *   docs.html?doc=design/architecture#doc=design%2Farchitecture&lang=en&dl=zh-CN&h=2-component-responsibilities
 *
 * Everything degrades gracefully: a missing manifest, a failed fetch, an
 * unreachable diagram and an offline viewer all fall back to readable content.
 */
(function (global) {
  "use strict";

  var DOCS_BASE = "docs/";
  var MANIFEST_URL = DOCS_BASE + "manifest.json";
  var REPO = "https://github.com/go-taas/go-taas";
  var BRANCH = "main";
  // Resolved against the document base URL: a bare specifier would be treated
  // as a package name and fail to resolve from a classic script.
  var MERMAID_URL = new URL("vendor/mermaid/mermaid.esm.min.mjs", document.baseURI).href;
  var LANGS = ["en", "zh-CN"];
  var FALLBACK_DOC = "design/architecture";
  var HEADER_OFFSET = 150;

  var dom = {
    nav: document.getElementById("docs-nav"),
    navLists: document.getElementById("docs-nav-lists"),
    navEmpty: document.getElementById("docs-nav-empty"),
    count: document.getElementById("docs-count"),
    search: document.getElementById("docs-search"),
    searchClear: document.getElementById("docs-search-clear"),
    menuBtn: document.getElementById("docs-menu-btn"),
    progress: document.getElementById("docs-progress"),
    crumbs: document.getElementById("docs-crumbs"),
    head: document.getElementById("docs-head"),
    title: document.getElementById("docs-title"),
    facts: document.getElementById("docs-facts"),
    langBtn: document.getElementById("docs-lang"),
    langLabel: document.getElementById("docs-lang-label"),
    edit: document.getElementById("docs-edit"),
    state: document.getElementById("docs-state"),
    stateText: document.getElementById("docs-state-text"),
    body: document.getElementById("docs-body"),
    pager: document.getElementById("docs-pager"),
    toc: document.getElementById("docs-toc"),
    tocList: document.getElementById("docs-toc-list"),
    top: document.getElementById("docs-top")
  };

  var state = {
    manifest: null,
    entries: [],     // documents plus OpenAPI specs, in manifest order
    byId: new Map(),
    uiLang: "en",
    docLang: "en",
    doc: null,
    variant: null,
    markdown: "",
    query: "",
    anchor: null,
    tocLinks: new Map(),
    mermaid: null,
    token: 0
  };

  /* ---------------------------------------------------------------- helpers */

  function t(key, vars) {
    var table = (global.SITE_I18N || {})[state.uiLang] || (global.SITE_I18N || {}).en || {};
    var value = typeof table[key] === "string" ? table[key] : key;
    if (vars) {
      Object.keys(vars).forEach(function (name) {
        value = value.replace(new RegExp("\\{" + name + "\\}", "g"), vars[name]);
      });
    }
    return value;
  }

  function otherLang(lang) {
    return lang === "zh-CN" ? "en" : "zh-CN";
  }

  function slugify(text) {
    return String(text)
      .replace(/<[^>]*>/g, "")
      .replace(/`/g, "")
      .trim()
      .toLowerCase()
      .replace(/[^\p{L}\p{N}\p{M}\p{Pc}\- ]/gu, "")
      .replace(/ +/g, "-");
  }

  /** Collapses ./ and ../ segments in a repository-relative path. */
  function resolvePath(path) {
    var parts = [];
    String(path).split("/").forEach(function (part) {
      if (part === "." || part === "") return;
      if (part === "..") parts.pop();
      else parts.push(part);
    });
    return parts.join("/");
  }

  function readHash() {
    var out = {};
    String(global.location.hash || "").replace(/^#/, "").split("&").forEach(function (pair) {
      var at = pair.indexOf("=");
      if (at <= 0) return;
      out[decodeURIComponent(pair.slice(0, at))] = decodeURIComponent(pair.slice(at + 1));
    });
    return out;
  }

  function writeHash(replace) {
    var parts = [
      "doc=" + encodeURIComponent(state.doc ? state.doc.id : FALLBACK_DOC),
      "lang=" + encodeURIComponent(state.uiLang)
    ];
    if (state.docLang !== state.uiLang) parts.push("dl=" + encodeURIComponent(state.docLang));
    if (state.anchor) parts.push("h=" + encodeURIComponent(state.anchor));
    var next = "#" + parts.join("&");
    var current = decodeURIComponent(String(global.location.hash || ""));
    if (current === decodeURIComponent(next)) return;
    var url = global.location.pathname + global.location.search + next;
    global.history[replace ? "replaceState" : "pushState"](null, "", url);
  }

  /* --------------------------------------------------------------- manifest */

  function buildIndex(manifest) {
    var entries = [];
    var byId = new Map();

    (manifest.sections || []).forEach(function (section) {
      if (section.docs) {
        section.docs.forEach(function (entry) {
          var variants = {};
          LANGS.forEach(function (lang) {
            if (entry[lang]) variants[lang] = entry[lang];
          });
          var doc = { id: section.id + "/" + entry.key, section: section, variants: variants };
          entries.push(doc);
          byId.set(doc.id, doc);
        });
        return;
      }
      if (section.specs) {
        section.specs.forEach(function (spec) {
          var doc = { id: "api/" + spec.service, section: section, spec: spec, variants: {} };
          entries.push(doc);
          byId.set(doc.id, doc);
        });
      }
    });

    state.entries = entries;
    state.byId = byId;
  }

  function sectionLabel(section, lang) {
    return (section.title && (section.title[lang] || section.title.en)) || section.id;
  }

  /** The document's title in one language, falling back to the other. */
  function docTitle(doc, lang) {
    if (doc.spec) return doc.spec.title;
    var variant = doc.variants[lang] || doc.variants[otherLang(lang)];
    return variant ? variant.title : doc.id;
  }

  /** Returns the variant to display for a requested document language. */
  function pickVariant(doc, lang) {
    if (!doc || doc.spec) return null;
    return doc.variants[lang] || doc.variants[otherLang(lang)] || null;
  }

  function hasVariant(doc, lang) {
    return Boolean(doc && doc.variants && doc.variants[lang]);
  }

  /* ---------------------------------------------------------------- sidebar */

  function renderSidebar() {
    var query = state.query.trim().toLowerCase();
    var fragment = document.createDocumentFragment();
    var groups = 0;

    (state.manifest.sections || []).forEach(function (section) {
      var matches = state.entries.filter(function (doc) {
        return doc.section === section && matchesQuery(doc, query);
      });
      if (matches.length === 0) return;
      groups += 1;

      var group = document.createElement("section");
      group.className = "docs-nav-section";

      var head = document.createElement("p");
      head.className = "docs-nav-head";
      head.appendChild(document.createTextNode(sectionLabel(section, state.uiLang)));
      var badge = document.createElement("span");
      badge.className = "docs-nav-badge";
      badge.textContent = matches.length;
      head.appendChild(badge);
      group.appendChild(head);

      var list = document.createElement("ul");
      list.className = "docs-nav-list";
      matches.forEach(function (doc) {
        list.appendChild(navItem(doc, query));
      });
      group.appendChild(list);
      fragment.appendChild(group);
    });

    dom.navLists.innerHTML = "";
    dom.navLists.appendChild(fragment);
    dom.navEmpty.hidden = groups > 0;
    if (groups === 0) dom.navEmpty.textContent = t("docs.noResults", { query: state.query });

    var total = state.entries.length;
    dom.count.textContent = total === 1 ? t("docs.countOne", { count: total }) : t("docs.countMany", { count: total });
    markSidebarCurrent();
  }

  function matchesQuery(doc, query) {
    if (!query) return true;
    if (!doc._haystack) {
      var parts = [doc.id];
      if (doc.spec) {
        parts.push(doc.spec.service, doc.spec.title, doc.spec.proto);
        doc.spec.operations.forEach(function (operation) {
          parts.push(operation.method, operation.path, operation.summary);
        });
      }
      LANGS.forEach(function (lang) {
        var variant = doc.variants[lang];
        if (!variant) return;
        parts.push(variant.title, variant.scope || "", variant.featurePoint || "", variant.status || "", variant.owner || "");
        (variant.headings || []).forEach(function (heading) {
          parts.push(heading.text);
        });
      });
      doc._haystack = parts.join("\n").toLowerCase();
    }
    return doc._haystack.indexOf(query) >= 0;
  }

  function navItem(doc, query) {
    var item = document.createElement("li");
    var button = document.createElement("button");
    button.type = "button";
    button.className = "docs-nav-item";
    button.dataset.docId = doc.id;
    button.appendChild(highlight(docTitle(doc, state.uiLang), query));

    var meta = "";
    if (doc.spec) meta = t("docs.operations", { count: doc.spec.operations.length });
    else if (hasVariant(doc, "en") && hasVariant(doc, "zh-CN")) meta = doc.id.split("/").pop();
    if (meta) {
      var span = document.createElement("span");
      span.className = "docs-nav-meta";
      span.textContent = meta;
      button.appendChild(span);
    }
    if (state.doc && state.doc.id === doc.id) button.setAttribute("aria-current", "true");

    button.addEventListener("click", function () {
      go(doc.id);
    });
    item.appendChild(button);
    return item;
  }

  /** Wraps every case-insensitive occurrence of the query in a <mark>. */
  function highlight(text, query) {
    var container = document.createDocumentFragment();
    var value = String(text);
    if (!query) {
      container.appendChild(document.createTextNode(value));
      return container;
    }
    var lowered = value.toLowerCase();
    var at = 0;
    while (at < value.length) {
      var found = lowered.indexOf(query, at);
      if (found === -1) break;
      if (found > at) container.appendChild(document.createTextNode(value.slice(at, found)));
      var mark = document.createElement("mark");
      mark.textContent = value.slice(found, found + query.length);
      container.appendChild(mark);
      at = found + query.length;
    }
    if (at < value.length) container.appendChild(document.createTextNode(value.slice(at)));
    return container;
  }

  function markSidebarCurrent() {
    var buttons = dom.navLists.querySelectorAll(".docs-nav-item");
    for (var i = 0; i < buttons.length; i++) {
      if (state.doc && buttons[i].dataset.docId === state.doc.id) buttons[i].setAttribute("aria-current", "true");
      else buttons[i].removeAttribute("aria-current");
    }
    var current = dom.navLists.querySelector('[aria-current="true"]');
    if (!current || !dom.nav) return;
    var box = current.getBoundingClientRect();
    var nav = dom.nav.getBoundingClientRect();
    if (box.top < nav.top || box.bottom > nav.bottom) {
      dom.nav.scrollTop += box.top - nav.top - nav.height / 3;
    }
  }

  /* ----------------------------------------------------------------- states */

  function showLoading() {
    delete dom.state.dataset.variant;
    dom.state.hidden = false;
    dom.stateText.textContent = t("docs.loading");
    var retry = dom.state.querySelector(".docs-retry");
    if (retry) retry.remove();
  }

  function showError(detail) {
    dom.state.dataset.variant = "error";
    dom.state.hidden = false;
    dom.stateText.textContent = detail ? t("docs.error") + " (" + detail + ")" : t("docs.error");
    if (!dom.state.querySelector(".docs-retry")) {
      var retry = document.createElement("button");
      retry.type = "button";
      retry.className = "docs-retry";
      retry.textContent = t("docs.retry");
      retry.addEventListener("click", function () {
        if (state.doc) load(state.doc);
      });
      dom.state.appendChild(retry);
    }
  }

  function hideArticle() {
    dom.body.hidden = true;
    dom.body.innerHTML = "";
    dom.head.hidden = true;
    dom.facts.innerHTML = "";
    dom.tocList.innerHTML = "";
    state.tocLinks = new Map();
    if (observer) {
      observer.disconnect();
      observer = null;
    }
  }

  /* --------------------------------------------------------------- document */

  async function load(doc, options) {
    options = options || {};
    if (options.anchor !== undefined) state.anchor = options.anchor || null;
    state.doc = doc;
    state.docLang = options.docLang && LANGS.indexOf(options.docLang) >= 0 ? options.docLang : state.uiLang;
    var token = ++state.token;

    hideArticle();
    renderCrumbs(doc);
    markSidebarCurrent();
    renderPager(doc);
    if (!options.silent) writeHash(Boolean(options.replace));

    if (doc.spec) {
      state.variant = null;
      state.markdown = "";
      renderSpec(doc);
      return;
    }

    var variant = pickVariant(doc, state.docLang);
    if (!variant) {
      showError(doc.id);
      return;
    }
    state.variant = variant;
    state.docLang = variant.lang;
    showLoading();

    try {
      // The manifest drives navigation, so it is always revalidated; document
      // bodies use the default cache policy (the host's short TTL) so
      // back/forward navigation and revisits stay instant.
      var response = await fetch(DOCS_BASE + variant.path);
      if (!response.ok) throw new Error("HTTP " + response.status);
      var markdown = await response.text();
      if (token !== state.token) return;
      state.markdown = markdown;
      renderArticle(doc, variant, markdown);
    } catch (error) {
      if (token !== state.token) return;
      showError(variant.path);
    }
  }

  /** Navigates to a document (pushes history) and closes the mobile drawer. */
  function go(docId, anchor) {
    closeNav();
    var doc = state.byId.get(docId);
    if (!doc) return;
    load(doc, { anchor: anchor === undefined ? null : anchor });
  }

  function renderArticle(doc, variant, markdown) {
    var rendered = renderMarkdown(markdown, variant);

    dom.title.textContent = variant.title || doc.id;
    dom.facts.innerHTML = "";
    if (rendered.facts) dom.facts.appendChild(rendered.facts);
    else renderFacts(variant);
    renderLangButton(doc, variant);
    dom.edit.href = REPO + "/blob/" + BRANCH + "/site/docs/" + variant.path;

    dom.body.innerHTML = "";
    dom.body.appendChild(rendered.fragment);
    dom.body.hidden = false;
    dom.head.hidden = false;
    dom.state.hidden = true;

    renderToc(rendered.headings);
    watchHeadings();
    drawDiagrams();

    var anchor = state.anchor;
    state.anchor = null;
    if (anchor) scrollToAnchor(anchor);
    else global.scrollTo({ top: 0, behavior: "auto" });
  }

  /* --------------------------------------------------------------- markdown */

  function renderMarkdown(markdown, variant) {
    // Extract Mermaid fences first: their bodies are not Markdown and must
    // survive parsing verbatim.
    var diagrams = [];
    var prepared = String(markdown).replace(/```mermaid\r?\n([\s\S]*?)```/g, function (_, code) {
      var index = diagrams.push(code.replace(/\n$/, "")) - 1;
      return "\n\n@@GO_TAAS_MERMAID_" + index + "@@\n\n";
    });

    var html = global.marked.parse(prepared, { gfm: true, breaks: false });
    var holder = document.createElement("div");
    holder.innerHTML = html;

    // The leading metadata table becomes a definition-list card: it carries the
    // feature point, scope, status, owner and the cross-references to the other
    // documents, which must stay clickable.
    var facts = null;
    var firstTable = holder.querySelector("table");
    if (firstTable && isMetadataTable(firstTable)) {
      facts = metadataCard(firstTable);
      firstTable.parentNode.replaceChild(facts, firstTable);
    }

    var headings = normalizeHeadings(holder);
    rewriteLinks(holder, variant);
    decorateCode(holder);
    substituteDiagrams(holder, diagrams);

    if (facts && facts.parentNode) facts.parentNode.removeChild(facts);

    var fragment = document.createDocumentFragment();
    while (holder.firstChild) fragment.appendChild(holder.firstChild);
    return { fragment: fragment, headings: headings, facts: facts };
  }

  function isMetadataTable(table) {
    var row = table.querySelector("thead tr");
    if (!row) return false;
    var text = row.textContent.replace(/\s+/g, " ").trim().toLowerCase();
    return text === "attribute content" || text === "属性 内容";
  }

  /** Converts the metadata table into the article's fact card. */
  function metadataCard(table) {
    var card = document.createElement("dl");
    card.className = "docs-facts";
    var rows = table.querySelectorAll("tbody tr");
    for (var i = 0; i < rows.length; i++) {
      var cells = rows[i].children;
      if (cells.length < 2) continue;
      var label = cells[0].textContent.trim();
      if (!label) continue;

      var wrap = document.createElement("div");
      if (/related documents|相关文档|document scope|文档范围/i.test(label)) {
        wrap.className = "docs-facts-wide";
      }
      var dt = document.createElement("dt");
      dt.textContent = label;
      var dd = document.createElement("dd");
      if (wrap.className) dd.className = "docs-facts-wide";
      dd.innerHTML = cells[1].innerHTML;
      wrap.appendChild(dt);
      wrap.appendChild(dd);
      card.appendChild(wrap);
    }
    return card;
  }

  /** Adds stable ids and hover anchor links, dropping the duplicated H1. */
  function normalizeHeadings(root) {
    var headings = [];
    var seen = new Map();
    var nodes = Array.prototype.slice.call(root.querySelectorAll("h1, h2, h3, h4"));

    nodes.forEach(function (node) {
      var level = Number(node.tagName.slice(1));
      if (level === 1) {
        node.remove(); // the title is already rendered in the article header
        return;
      }
      if (level > 3) return;

      var text = node.textContent.trim();
      var base = slugify(text);
      var count = seen.get(base) || 0;
      seen.set(base, count + 1);
      node.id = node.id || (count === 0 ? base : base + "-" + count);
      headings.push({ level: level, text: text, id: node.id });

      var anchor = document.createElement("a");
      anchor.className = "docs-anchor";
      anchor.href = "#" + node.id;
      anchor.setAttribute("aria-label", text);
      anchor.textContent = "#";
      anchor.addEventListener("click", function (event) {
        event.preventDefault();
        state.anchor = node.id;
        writeHash(true);
        scrollToAnchor(node.id);
      });
      node.appendChild(anchor);
    });

    return headings;
  }

  /** Rewrites repository-relative links so they stay inside the browser. */
  function rewriteLinks(root, variant) {
    var dir = variant.path.replace(/[^/]+$/, "");
    Array.prototype.slice.call(root.querySelectorAll("a[href]")).forEach(function (link) {
      var href = link.getAttribute("href");
      if (!href) return;

      if (href.charAt(0) === "#") {
        var anchorId = href.slice(1);
        link.addEventListener("click", function (event) {
          event.preventDefault();
          state.anchor = anchorId;
          writeHash(true);
          scrollToAnchor(anchorId);
        });
        return;
      }

      if (/^[a-z][a-z0-9+.-]*:/i.test(href) || href.indexOf("//") === 0) {
        external(link);
        return;
      }

      var resolved = resolvePath(dir + href);
      var anchor = "";
      var hashAt = resolved.indexOf("#");
      if (hashAt >= 0) {
        anchor = resolved.slice(hashAt + 1);
        resolved = resolved.slice(0, hashAt);
      }

      if (!/\.md$/i.test(resolved)) {
        link.href = REPO + "/blob/" + BRANCH + "/" + resolved;
        external(link);
        return;
      }

      // A link that names a language variant opens that variant: that is how
      // the EN and ZH document sets cross-reference each other.
      var docLang = /\.zh-cn\.md$/i.test(resolved) ? "zh-CN" : "en";
      var targetId = resolveDocId(variant, resolved);
      if (!targetId) {
        link.href = REPO + "/blob/" + BRANCH + "/" + resolved;
        external(link);
        return;
      }

      link.href = "docs.html?doc=" + encodeURIComponent(targetId);
      link.addEventListener("click", function (event) {
        event.preventDefault();
        closeNav();
        load(state.byId.get(targetId), { docLang: docLang, anchor: anchor || null });
      });
    });
  }

  function external(link) {
    link.classList.add("docs-ext");
    link.target = "_blank";
    link.rel = "noopener";
  }

  /** Maps a Markdown link target onto a known document id. */
  function resolveDocId(variant, resolved) {
    var raw = resolved.replace(/^site\/docs\//, "");
    var key = raw.replace(/\.zh-cn\.md$/i, "").replace(/\.md$/i, "");

    if (state.byId.has(key)) return key;

    var tail = key.split("/").pop();
    var candidates = state.entries.filter(function (doc) {
      return doc.id.split("/").pop() === tail;
    });
    if (candidates.length === 1) return candidates[0].id;
    var sameSection = candidates.filter(function (doc) {
      return variant.path.indexOf(doc.section.id + "/") === 0;
    });
    return sameSection.length > 0 ? sameSection[0].id : null;
  }

  /** Wraps every code fence in a titled, copyable, highlighted block. */
  function decorateCode(root) {
    Array.prototype.slice.call(root.querySelectorAll("pre")).forEach(function (pre) {
      var code = pre.querySelector("code");
      if (!code) return;

      var language = "";
      (code.className || "").split(/\s+/).some(function (name) {
        if (name.indexOf("language-") !== 0) return false;
        language = name.slice("language-".length);
        return true;
      });

      if (language && global.hljs && global.hljs.getLanguage(language)) {
        try {
          code.innerHTML = global.hljs.highlight(code.textContent, { language: language }).value;
        } catch (e) {
          /* keep the plain text */
        }
      }

      var wrapper = document.createElement("div");
      wrapper.className = "docs-code";

      var bar = document.createElement("div");
      bar.className = "docs-code-bar";
      var lang = document.createElement("span");
      lang.className = "docs-code-lang";
      lang.textContent = language || "text";
      bar.appendChild(lang);

      var copy = document.createElement("button");
      copy.type = "button";
      copy.className = "docs-code-copy";
      copy.textContent = t("docs.copyCode");
      copy.addEventListener("click", function () {
        copyText(code.textContent).then(function (ok) {
          copy.dataset.copied = ok ? "true" : "false";
          copy.textContent = ok ? t("docs.copied") : t("docs.copyCode");
          global.setTimeout(function () {
            copy.dataset.copied = "false";
            copy.textContent = t("docs.copyCode");
          }, 1600);
        });
      });
      bar.appendChild(copy);

      pre.parentNode.insertBefore(wrapper, pre);
      wrapper.appendChild(bar);
      wrapper.appendChild(pre);
    });
  }

  function copyText(text) {
    if (global.navigator.clipboard && global.navigator.clipboard.writeText) {
      return global.navigator.clipboard.writeText(text).then(
        function () {
          return true;
        },
        function () {
          return false;
        }
      );
    }
    try {
      var area = document.createElement("textarea");
      area.value = text;
      area.setAttribute("readonly", "");
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.appendChild(area);
      area.select();
      var ok = document.execCommand("copy");
      area.remove();
      return Promise.resolve(ok);
    } catch (e) {
      return Promise.resolve(false);
    }
  }

  /** Swaps the extracted Mermaid placeholders for diagram figures. */
  function substituteDiagrams(root, diagrams) {
    Array.prototype.slice.call(root.querySelectorAll("p")).forEach(function (paragraph) {
      var match = /^@@GO_TAAS_MERMAID_(\d+)@@$/.exec(paragraph.textContent.trim());
      if (!match) return;

      var figure = document.createElement("figure");
      figure.className = "docs-diagram";
      figure.dataset.raw = diagrams[Number(match[1])] || "";
      figure.dataset.index = match[1];

      var bar = document.createElement("div");
      bar.className = "docs-diagram-bar";
      var kind = document.createElement("span");
      kind.className = "docs-diagram-kind";
      kind.textContent = diagramKind(figure.dataset.raw);
      bar.appendChild(kind);
      figure.appendChild(bar);

      var body = document.createElement("div");
      body.className = "docs-diagram-body";
      figure.appendChild(body);

      paragraph.parentNode.replaceChild(figure, paragraph);
    });
  }

  /** Reads the diagram type from the first non-empty source line. */
  function diagramKind(source) {
    var match = /^\s*(\S+)/.exec(String(source || ""));
    return match ? match[1] : "diagram";
  }

  /** Renders every diagram in the current article, loading Mermaid lazily. */
  async function drawDiagrams() {
    var figures = Array.prototype.slice.call(dom.body.querySelectorAll(".docs-diagram"));
    if (figures.length === 0) return;

    var mermaid;
    try {
      mermaid = await loadMermaid();
    } catch (error) {
      console.warn("docs: mermaid unavailable", error);
      figures.forEach(function (figure) {
        showDiagramSource(figure, figure.dataset.raw || "");
      });
      return;
    }

    for (var i = 0; i < figures.length; i++) {
      var figure = figures[i];
      var source = figure.dataset.raw || "";
      try {
        var result = await mermaid.render("go-taas-diagram-" + figure.dataset.index + "-" + i, source);
        figure.querySelector(".docs-diagram-body").innerHTML = result.svg;
        capDiagramHeight(figure);
        addDiagramActions(figure);
      } catch (error) {
        showDiagramSource(figure, source);
      }
    }
  }

  /**
   * Diagrams wider or taller than the reading column keep their natural size and
   * scroll, so labels stay legible; only very tall ones are height-capped to
   * keep the article navigable.
   */
  function capDiagramHeight(figure) {
    var svg = figure.querySelector(".docs-diagram-body svg");
    if (!svg) return;
    if (svg.getBoundingClientRect().height > global.innerHeight * 1.6) {
      figure.classList.add("docs-diagram--scroll");
    }
  }

  /** Adds the fullscreen affordance to a successfully rendered diagram. */
  function addDiagramActions(figure) {
    var bar = figure.querySelector(".docs-diagram-bar");
    var button = document.createElement("button");
    button.type = "button";
    button.className = "docs-diagram-max";
    button.title = t("docs.fullscreen");
    button.setAttribute("aria-label", t("docs.fullscreen"));
    button.innerHTML =
      '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.9" ' +
      'stroke-linecap="round" aria-hidden="true"><path d="M4 9V4h5M20 15v5h-5M15 4h5v5M9 20H4v-5"/></svg>' +
      "<span>" + t("docs.fullscreen") + "</span>";
    button.addEventListener("click", function () {
      openLightbox(figure);
    });
    bar.appendChild(button);
  }

  /** Shows one diagram at full size in an overlay. */
  function openLightbox(figure) {
    var overlay = document.createElement("div");
    overlay.className = "docs-lightbox";
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");

    var panel = document.createElement("div");
    panel.className = "docs-lightbox-panel";

    var bar = document.createElement("div");
    bar.className = "docs-lightbox-bar";
    var label = document.createElement("span");
    label.textContent = figure.querySelector(".docs-diagram-kind").textContent;
    bar.appendChild(label);

    var close = document.createElement("button");
    close.type = "button";
    close.className = "docs-lightbox-close";
    close.setAttribute("aria-label", t("docs.closeMenu"));
    close.textContent = "×";
    bar.appendChild(close);
    panel.appendChild(bar);

    var stage = document.createElement("div");
    stage.className = "docs-lightbox-stage";
    var svg = figure.querySelector(".docs-diagram-body svg");
    if (svg) stage.appendChild(svg.cloneNode(true));
    panel.appendChild(stage);
    overlay.appendChild(panel);
    document.body.appendChild(overlay);

    function dismiss() {
      overlay.remove();
      document.removeEventListener("keydown", onKey);
      document.body.style.removeProperty("overflow");
    }
    function onKey(event) {
      if (event.key === "Escape") dismiss();
    }
    close.addEventListener("click", dismiss);
    overlay.addEventListener("click", function (event) {
      if (event.target === overlay) dismiss();
    });
    document.addEventListener("keydown", onKey);
    document.body.style.overflow = "hidden";
    close.focus();
    // Diagrams are usually wider than the viewport: start centered on the top
    // edge instead of the top-left corner. The clone needs one layout pass
    // before it can be scrolled, so this runs on the next frame.
    global.requestAnimationFrame(function () {
      stage.scrollLeft = Math.max((stage.scrollWidth - stage.clientWidth) / 2, 0);
    });
  }

  function showDiagramSource(figure, source) {
    figure.dataset.failed = "true";
    var body = figure.querySelector(".docs-diagram-body");
    body.innerHTML = "";
    var pre = document.createElement("pre");
    var code = document.createElement("code");
    if (global.hljs && global.hljs.getLanguage("mermaid")) {
      code.innerHTML = global.hljs.highlight(source, { language: "mermaid" }).value;
    } else {
      code.textContent = source;
    }
    code.style.fontFamily = "var(--mono)";
    code.style.fontSize = "12.8px";
    code.style.color = "var(--code-fg)";
    pre.appendChild(code);
    body.appendChild(pre);
    if (!figure.querySelector(".docs-diagram-note")) {
      var note = document.createElement("p");
      note.className = "docs-diagram-note";
      note.textContent = t("docs.diagramsFailed");
      figure.appendChild(note);
    }
  }

  /** Loads and configures Mermaid exactly once, on demand. */
  function loadMermaid() {
    if (!state.mermaid) {
      state.mermaid = import(MERMAID_URL).then(function (module) {
        var mermaid = module.default;
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          theme: "base",
          fontFamily: getComputedStyle(document.body).fontFamily,
          themeVariables: {
            primaryColor: "#edf7f7",
            primaryBorderColor: "#4e9ca0",
            primaryTextColor: "#17272b",
            lineColor: "#8aa6a9",
            secondaryColor: "#fdf2ed",
            tertiaryColor: "#f5f8f9",
            noteBkgColor: "#fbe7cf",
            noteBorderColor: "#d08a3c",
            actorBkg: "#edf7f7",
            actorBorder: "#4e9ca0",
            signalColor: "#47595e",
            signalTextColor: "#17272b"
          },
          flowchart: { curve: "basis", htmlLabels: true, useMaxWidth: false },
          sequence: { useMaxWidth: false, wrap: false, boxMargin: 8 },
          state: { useMaxWidth: false }
        });
        return mermaid;
      });
      // A failed import must not be retried for every diagram on the page.
      state.mermaid.catch(function () {});
    }
    return state.mermaid;
  }

  /* ------------------------------------------------------------------ facts */

  var FACT_LABELS = {
    featurePoint: { en: "Feature point", "zh-CN": "功能点" },
    scope: { en: "Document scope", "zh-CN": "文档范围" },
    status: { en: "Status", "zh-CN": "状态" },
    owner: { en: "Owning module", "zh-CN": "所属模块" },
    sections: { en: "Sections", "zh-CN": "章节" }
  };

  function factLabel(key) {
    var entry = FACT_LABELS[key];
    return (entry && (entry[state.uiLang] || entry.en)) || key;
  }

  function renderFacts(variant) {
    dom.facts.innerHTML = "";
    var rows = [
      ["featurePoint", variant.featurePoint],
      ["scope", variant.scope],
      ["status", variant.status],
      ["owner", variant.owner]
    ].filter(function (row) {
      return Boolean(row[1]);
    });

    if (rows.length === 0) {
      // Documents such as the platform architecture overview carry no metadata
      // table; their top-level section list is shown instead.
      var top = (variant.headings || [])
        .filter(function (heading) {
          return heading.level === 2;
        })
        .map(function (heading) {
          return heading.text;
        });
      if (top.length > 0) rows.push(["sections", top.join(" · ")]);
    }

    rows.forEach(function (row) {
      var wide = row[0] === "scope" || row[0] === "sections";
      var wrap = document.createElement("div");
      if (wide) wrap.className = "docs-facts-wide";
      var dt = document.createElement("dt");
      dt.textContent = factLabel(row[0]);
      var dd = document.createElement("dd");
      if (wide) dd.className = "docs-facts-wide";
      dd.textContent = row[1];
      wrap.appendChild(dt);
      wrap.appendChild(dd);
      dom.facts.appendChild(wrap);
    });
  }

  function renderLangButton(doc, variant) {
    var other = otherLang(variant.lang);
    if (!hasVariant(doc, other)) {
      dom.langBtn.hidden = true;
      return;
    }
    dom.langBtn.hidden = false;
    dom.langLabel.textContent = t("docs.otherLanguage");
    dom.langBtn.onclick = function () {
      load(doc, { docLang: other, anchor: state.anchor });
    };
  }

  /* -------------------------------------------------------------------- toc */

  function renderToc(headings) {
    dom.tocList.innerHTML = "";
    state.tocLinks = new Map();
    headings.forEach(function (heading) {
      var item = document.createElement("li");
      var link = document.createElement("a");
      link.href = "#" + heading.id;
      link.dataset.depth = String(heading.level);
      link.textContent = heading.text;
      link.addEventListener("click", function (event) {
        event.preventDefault();
        state.anchor = heading.id;
        writeHash(true);
        scrollToAnchor(heading.id);
      });
      item.appendChild(link);
      dom.tocList.appendChild(item);
      state.tocLinks.set(heading.id, link);
    });
  }

  var observer = null;

  function watchHeadings() {
    if (observer) observer.disconnect();
    if (!("IntersectionObserver" in global) || state.tocLinks.size === 0) return;
    observer = new IntersectionObserver(
      function () {
        var active = null;
        state.tocLinks.forEach(function (link, id) {
          var heading = document.getElementById(id);
          if (heading && heading.getBoundingClientRect().top < HEADER_OFFSET + 12) active = id;
        });
        if (!active) {
          var first = state.tocLinks.keys().next();
          active = first.done ? null : first.value;
        }
        state.tocLinks.forEach(function (link, id) {
          link.classList.toggle("is-active", id === active);
        });
      },
      { rootMargin: "-" + HEADER_OFFSET + "px 0px -72% 0px" }
    );
    state.tocLinks.forEach(function (link, id) {
      var heading = document.getElementById(id);
      if (heading) observer.observe(heading);
    });
  }

  function scrollToAnchor(id) {
    var target = document.getElementById(id);
    if (!target) return;
    var top = target.getBoundingClientRect().top + global.scrollY - HEADER_OFFSET;
    global.scrollTo({ top: Math.max(top, 0), behavior: "auto" });
  }

  /* ------------------------------------------------------------------ pager */

  function renderPager(doc) {
    var index = state.entries.indexOf(doc);
    var previous = index > 0 ? state.entries[index - 1] : null;
    var next = index >= 0 && index < state.entries.length - 1 ? state.entries[index + 1] : null;

    dom.pager.innerHTML = "";
    dom.pager.hidden = !previous && !next;
    if (previous) dom.pager.appendChild(pagerLink(previous, "docs.previous", false));
    if (next) dom.pager.appendChild(pagerLink(next, "docs.next", true));
  }

  function pagerLink(doc, directionKey, isNext) {
    var link = document.createElement("a");
    if (isNext) link.className = "docs-pager-next";
    var dir = document.createElement("span");
    dir.className = "docs-pager-dir";
    dir.textContent = (isNext ? "→ " : "← ") + t(directionKey);
    var title = document.createElement("span");
    title.className = "docs-pager-title";
    title.textContent = docTitle(doc, state.uiLang);
    link.appendChild(dir);
    link.appendChild(title);
    link.href = "docs.html?doc=" + encodeURIComponent(doc.id);
    link.addEventListener("click", function (event) {
      event.preventDefault();
      go(doc.id);
    });
    return link;
  }

  function renderCrumbs(doc) {
    dom.crumbs.innerHTML = "";
    if (!doc.section) return;

    var root = document.createElement("button");
    root.type = "button";
    root.textContent = t("docs.title");
    root.addEventListener("click", function () {
      go(FALLBACK_DOC);
    });
    dom.crumbs.appendChild(root);

    var sep = document.createElement("span");
    sep.className = "docs-crumbs-sep";
    sep.textContent = "/";
    dom.crumbs.appendChild(sep);

    var section = document.createElement("span");
    section.textContent = sectionLabel(doc.section, state.uiLang);
    dom.crumbs.appendChild(section);

    var sep2 = document.createElement("span");
    sep2.className = "docs-crumbs-sep";
    sep2.textContent = "/";
    dom.crumbs.appendChild(sep2);

    var current = document.createElement("span");
    current.className = "docs-crumbs-current";
    current.textContent = docTitle(doc, state.docLang);
    dom.crumbs.appendChild(current);
  }

  /* ---------------------------------------------------------- openapi specs */

  var API_COLUMNS = {
    Method: { en: "Method", "zh-CN": "方法" },
    Path: { en: "Path", "zh-CN": "路径" },
    Summary: { en: "Summary", "zh-CN": "说明" }
  };

  function apiColumnLabel(text) {
    var entry = API_COLUMNS[text];
    return (entry && (entry[state.uiLang] || entry.en)) || text;
  }

  function renderSpec(doc) {
    var spec = doc.spec;
    var fragment = document.createDocumentFragment();

    dom.title.textContent = spec.title;
    dom.facts.innerHTML = "";
    dom.langBtn.hidden = true;
    dom.edit.href = REPO + "/tree/" + BRANCH + "/proto/taas/" + spec.service;

    var note = document.createElement("p");
    note.className = "docs-api-note";
    note.textContent = t("docs.apiGenerated");
    fragment.appendChild(note);

    var group = document.createElement("section");
    group.className = "docs-api-group";
    group.id = "api-operations";

    var meta = document.createElement("p");
    meta.className = "docs-api-version";
    meta.textContent = spec.proto + " · " + t("docs.operations", { count: spec.operations.length });
    group.appendChild(meta);

    var table = document.createElement("table");
    table.className = "docs-api-ops";
    var thead = document.createElement("thead");
    var headRow = document.createElement("tr");
    ["Method", "Path", "Summary"].forEach(function (text) {
      var th = document.createElement("th");
      th.textContent = apiColumnLabel(text);
      headRow.appendChild(th);
    });
    thead.appendChild(headRow);
    table.appendChild(thead);

    var tbody = document.createElement("tbody");
    spec.operations.forEach(function (operation) {
      var row = document.createElement("tr");
      var method = document.createElement("td");
      var badge = document.createElement("span");
      badge.className = "docs-method";
      badge.dataset.method = operation.method;
      badge.textContent = operation.method;
      method.appendChild(badge);
      row.appendChild(method);

      var path = document.createElement("td");
      var code = document.createElement("code");
      code.textContent = operation.path;
      path.appendChild(code);
      row.appendChild(path);

      var summary = document.createElement("td");
      summary.textContent = operation.summary;
      row.appendChild(summary);
      tbody.appendChild(row);
    });
    table.appendChild(tbody);
    group.appendChild(table);
    fragment.appendChild(group);

    dom.body.innerHTML = "";
    dom.body.appendChild(fragment);
    dom.body.hidden = false;
    dom.head.hidden = false;
    dom.state.hidden = true;
    renderToc([{ level: 2, text: spec.title, id: "api-operations" }]);
    watchHeadings();
    global.scrollTo({ top: 0, behavior: "auto" });
  }

  /* ------------------------------------------------------------------ chrome */

  function setupSearch() {
    var timer = null;
    dom.search.addEventListener("input", function () {
      global.clearTimeout(timer);
      timer = global.setTimeout(function () {
        state.query = dom.search.value;
        dom.searchClear.hidden = state.query.length === 0;
        renderSidebar();
      }, 90);
    });
    dom.search.addEventListener("keydown", function (event) {
      if (event.key === "Enter") {
        var first = dom.navLists.querySelector(".docs-nav-item");
        if (first) {
          event.preventDefault();
          first.click();
        }
      }
      if (event.key === "Escape") clearSearch();
    });
    dom.searchClear.addEventListener("click", clearSearch);
    applyAttributeLabels();

    document.addEventListener("keydown", function (event) {
      var tag = (event.target.tagName || "").toLowerCase();
      if (tag === "input" || tag === "textarea" || event.metaKey || event.ctrlKey || event.altKey) return;
      if (event.key === "/") {
        event.preventDefault();
        dom.search.focus();
        dom.search.select();
      }
    });

    dom.menuBtn.addEventListener("click", toggleNav);
  }

  function clearSearch() {
    dom.search.value = "";
    state.query = "";
    dom.searchClear.hidden = true;
    renderSidebar();
  }

  /** Applies the localized labels that are set as attributes, not as text. */
  function applyAttributeLabels() {
    dom.searchClear.title = t("docs.searchClear");
    dom.searchClear.setAttribute("aria-label", t("docs.searchClear"));
    dom.menuBtn.setAttribute("aria-label", t("docs.menu"));
    dom.top.setAttribute("aria-label", t("docs.backToTop"));
    dom.toc.setAttribute("aria-label", t("docs.onThisPage"));
    var hint = document.querySelector(".docs-search-hint");
    if (hint) hint.title = t("docs.searchHint");
  }

  function toggleNav() {
    var isOpen = dom.nav.classList.toggle("is-open");
    dom.menuBtn.setAttribute("aria-expanded", isOpen ? "true" : "false");
    var scrim = document.querySelector(".docs-scrim");
    if (isOpen && !scrim) {
      scrim = document.createElement("div");
      scrim.className = "docs-scrim";
      scrim.addEventListener("click", closeNav);
      document.body.appendChild(scrim);
    } else if (!isOpen && scrim) {
      scrim.remove();
    }
  }

  function closeNav() {
    dom.nav.classList.remove("is-open");
    dom.menuBtn.setAttribute("aria-expanded", "false");
    var scrim = document.querySelector(".docs-scrim");
    if (scrim) scrim.remove();
  }

  function setupProgress() {
    var ticking = false;
    function update() {
      ticking = false;
      var scrollable = document.documentElement.scrollHeight - global.innerHeight;
      var ratio = scrollable > 0 ? Math.min(global.scrollY / scrollable, 1) : 0;
      dom.progress.style.width = (ratio * 100).toFixed(2) + "%";
    }
    global.addEventListener(
      "scroll",
      function () {
        if (ticking) return;
        ticking = true;
        global.requestAnimationFrame(update);
      },
      { passive: true }
    );
    update();
  }

  function setupTop() {
    dom.top.addEventListener("click", function () {
      global.scrollTo({ top: 0, behavior: "smooth" });
    });
  }

  /**
   * Re-renders the current view when the UI language changes. The document
   * follows the UI language unless the reader pinned this one document to the
   * other language with the document-language button.
   */
  function setupLanguageSync() {
    document.addEventListener("go-taas:lang", function (event) {
      state.uiLang = event.detail.lang;
      renderSidebar();
      applyAttributeLabels();
      if (!state.doc) return;

      var wanted = state.docLang === state.uiLang ? state.uiLang : state.docLang;
      var variant = pickVariant(state.doc, wanted);
      if (variant && state.variant && variant.path === state.variant.path && state.markdown) {
        // Same source document: re-render the chrome in the new language.
        renderArticle(state.doc, variant, state.markdown);
        state.anchor = null;
      } else {
        load(state.doc, { docLang: wanted, silent: true, anchor: null });
      }
      renderCrumbs(state.doc);
      renderPager(state.doc);
      markSidebarCurrent();
    });
  }

  function setupHistory() {
    global.addEventListener("popstate", function () {
      var hash = readHash();
      if (!hash.doc) return;
      var doc = state.byId.get(hash.doc);
      if (!doc) return;
      var docLang = hash.dl && LANGS.indexOf(hash.dl) >= 0 ? hash.dl : state.docLang;
      load(doc, { anchor: hash.h || null, docLang: docLang, silent: true });
    });

    // A hash-only edit (a pasted or hand-edited link, or a cross-page jump) is a
    // same-document navigation: it fires neither load nor popstate, so the view
    // has to be re-synced here. main.js applies the language first and fires
    // go-taas:lang, which is why the hash wins over the stored preference.
    global.addEventListener("hashchange", function () {
      var hash = readHash();
      if (hash.lang && LANGS.indexOf(hash.lang) >= 0) state.uiLang = hash.lang;
      var doc = hash.doc ? state.byId.get(hash.doc) : null;
      if (!doc) return;
      var docLang = hash.dl && LANGS.indexOf(hash.dl) >= 0 ? hash.dl : state.uiLang;
      if (state.doc && state.doc.id === doc.id && docLang === state.docLang && !hash.h) return;
      load(doc, { anchor: hash.h || null, docLang: docLang, silent: true });
    });
  }

  /* -------------------------------------------------------------------- boot */

  async function boot() {
    var hash = readHash();
    state.uiLang = global.SITE_LANG ? global.SITE_LANG.get() : "en";
    if (hash.lang && LANGS.indexOf(hash.lang) >= 0) state.uiLang = hash.lang;
    state.docLang = state.uiLang;

    setupSearch();
    setupProgress();
    setupTop();
    setupLanguageSync();
    setupHistory();

    try {
      var response = await fetch(MANIFEST_URL, { cache: "no-cache" });
      if (!response.ok) throw new Error("HTTP " + response.status);
      state.manifest = await response.json();
    } catch (error) {
      showError("manifest.json");
      return;
    }

    buildIndex(state.manifest);
    renderSidebar();

    var query = new URLSearchParams(global.location.search);
    var docId = hash.doc || query.get("doc") || FALLBACK_DOC;
    var doc = state.byId.get(docId) || state.byId.get(FALLBACK_DOC);
    if (!doc) {
      showError(docId);
      return;
    }
    var docLang = hash.dl && LANGS.indexOf(hash.dl) >= 0 ? hash.dl : null;
    load(doc, { anchor: hash.h || null, docLang: docLang, replace: true });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})(window);
