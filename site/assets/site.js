/*
 * agent-brain site behavior. Progressive enhancement only: every feature here
 * is optional. Without JS, all tab panels stay visible, copy buttons stay
 * hidden, and the theme follows prefers-color-scheme with no toggle.
 * No external requests: the only fetch is the same-origin /releases/latest.json.
 */
(function () {
  "use strict";

  /* ------------------------------------------------------------------
     Theme toggle (early theme application happens in an inline head
     script; this wires up the visible toggle button).
     ------------------------------------------------------------------ */
  function initThemeToggle() {
    var btn = document.querySelector("[data-theme-toggle]");
    if (!btn) return;

    function resolvedIsDark() {
      var attr = document.documentElement.getAttribute("data-theme");
      if (attr === "dark") return true;
      if (attr === "light") return false;
      return !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
    }
    btn.setAttribute("aria-pressed", resolvedIsDark() ? "true" : "false");

    btn.addEventListener("click", function () {
      var root = document.documentElement;
      var current = root.getAttribute("data-theme");
      var next;
      if (current === "dark") {
        next = "light";
      } else if (current === "light") {
        next = "dark";
      } else {
        var prefersDark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
        next = prefersDark ? "light" : "dark";
      }
      root.setAttribute("data-theme", next);
      try { localStorage.setItem("agentbrain-theme", next); } catch (e) {}
      btn.setAttribute("aria-pressed", next === "dark" ? "true" : "false");
    });
  }

  /* ------------------------------------------------------------------
     Tabs: supports multiple independent tablists per page.
     ------------------------------------------------------------------ */
  function initTabs() {
    var tablists = document.querySelectorAll("[role=tablist]");
    tablists.forEach(function (tablist) {
      var tabs = Array.prototype.slice.call(tablist.querySelectorAll("[role=tab]"));
      var panels = tabs.map(function (t) {
        return document.getElementById(t.getAttribute("aria-controls"));
      });

      function activate(tab, focus) {
        tabs.forEach(function (t, i) {
          var selected = t === tab;
          t.setAttribute("aria-selected", selected ? "true" : "false");
          t.tabIndex = selected ? 0 : -1;
          if (!panels[i]) return;
          if (selected) panels[i].removeAttribute("hidden");
          else panels[i].setAttribute("hidden", "");
        });
        if (focus) tab.focus();
      }

      // JS is present: switch to real tab semantics and hide inactive panels.
      tabs.forEach(function (t, i) {
        t.setAttribute("aria-selected", i === 0 ? "true" : "false");
        t.tabIndex = i === 0 ? 0 : -1;
        if (!panels[i]) return;
        if (i === 0) panels[i].removeAttribute("hidden");
        else panels[i].setAttribute("hidden", "");
      });

      tablist.addEventListener("click", function (e) {
        var t = e.target.closest && e.target.closest("[role=tab]");
        if (t && tabs.indexOf(t) > -1) activate(t, false);
      });

      tablist.addEventListener("keydown", function (e) {
        var idx = tabs.indexOf(document.activeElement);
        if (idx < 0) return;
        var next = -1;
        if (e.key === "ArrowRight" || e.key === "ArrowDown") next = (idx + 1) % tabs.length;
        else if (e.key === "ArrowLeft" || e.key === "ArrowUp") next = (idx - 1 + tabs.length) % tabs.length;
        else if (e.key === "Home") next = 0;
        else if (e.key === "End") next = tabs.length - 1;
        if (next >= 0) {
          e.preventDefault();
          activate(tabs[next], true);
        }
      });

      // Preselect the Windows tab on a Windows UA, if present.
      try {
        var ua = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || navigator.userAgent || "";
        if (/win/i.test(ua)) {
          var winTab = tabs.filter(function (t) { return /win/i.test(t.textContent); })[0];
          if (winTab) activate(winTab, false);
        }
      } catch (e) {}
    });
  }

  /* ------------------------------------------------------------------
     Copy-to-clipboard buttons.
     ------------------------------------------------------------------ */
  function initCopyButtons() {
    var announce = document.getElementById("copy-announce");
    var btns = Array.prototype.slice.call(document.querySelectorAll(".copy-btn"));
    btns.forEach(function (btn) {
      btn.addEventListener("click", function () {
        var wrap = btn.closest(".code-wrap");
        var code = wrap && wrap.querySelector("code");
        if (!code) return;
        var text = code.textContent;

        function done() {
          btn.textContent = "Copied";
          btn.setAttribute("data-copied", "true");
          if (announce) announce.textContent = "Copied to clipboard";
          setTimeout(function () {
            btn.textContent = "Copy";
            btn.removeAttribute("data-copied");
            if (announce) announce.textContent = "";
          }, 1500);
        }

        function fallback() {
          try {
            var range = document.createRange();
            range.selectNodeContents(code);
            var sel = window.getSelection();
            sel.removeAllRanges();
            sel.addRange(range);
            document.execCommand("copy");
            sel.removeAllRanges();
            done();
          } catch (e) {
            btn.textContent = "Select & copy manually";
            setTimeout(function () { btn.textContent = "Copy"; }, 2000);
          }
        }

        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(done).catch(fallback);
        } else {
          fallback();
        }
      });
    });
  }

  /* ------------------------------------------------------------------
     Mobile main-nav toggle.
     ------------------------------------------------------------------ */
  function initNavToggle() {
    var btn = document.querySelector("[data-nav-toggle]");
    var nav = document.querySelector("[data-main-nav]");
    if (!btn || !nav) return;
    btn.addEventListener("click", function () {
      var open = nav.classList.toggle("is-open");
      btn.setAttribute("aria-expanded", open ? "true" : "false");
    });
  }

  /* ------------------------------------------------------------------
     Docs sidebar: mobile collapse toggle + scroll-spy highlighting.
     ------------------------------------------------------------------ */
  function initDocsSidebar() {
    var sidebar = document.querySelector("[data-docs-sidebar]");
    var toggle = document.querySelector("[data-docs-toc-toggle]");
    if (toggle && sidebar) {
      toggle.addEventListener("click", function () {
        var open = sidebar.classList.toggle("is-open");
        toggle.setAttribute("aria-expanded", open ? "true" : "false");
      });
    }
    if (!sidebar) return;
    var links = Array.prototype.slice.call(sidebar.querySelectorAll(".docs-nav a"));
    if (!links.length || !("IntersectionObserver" in window)) return;
    var sections = links
      .map(function (link) {
        var id = link.getAttribute("href").split("#")[1];
        return id ? document.getElementById(id) : null;
      })
      .filter(Boolean);
    if (!sections.length) return;

    var byId = {};
    links.forEach(function (link) {
      byId[link.getAttribute("href").split("#")[1]] = link;
    });

    var current = null;
    function setActive(id) {
      if (id === current) return;
      current = id;
      links.forEach(function (l) { l.classList.remove("is-active"); });
      if (byId[id]) byId[id].classList.add("is-active");
    }

    var observer = new IntersectionObserver(
      function (entries) {
        var visible = entries.filter(function (e) { return e.isIntersecting; });
        if (visible.length) {
          visible.sort(function (a, b) { return a.boundingClientRect.top - b.boundingClientRect.top; });
          setActive(visible[0].target.id);
        }
      },
      { rootMargin: "-15% 0px -70% 0px", threshold: 0 }
    );
    sections.forEach(function (s) { observer.observe(s); });
  }

  /* ------------------------------------------------------------------
     Latest release: fetch /releases/latest.json (same origin). Updates the
     version and date text, and fills the docs download table if present.
     Best-effort only: any failure leaves the static markup in place.
     All values are written with textContent, never as HTML.
     ------------------------------------------------------------------ */
  function fillAssetTable(tbody, data) {
    if (!tbody || !data.assets || !data.assets.length) return;
    var version = String(data.version).replace(/^v/, "");
    tbody.textContent = "";
    data.assets.forEach(function (asset) {
      var tr = document.createElement("tr");
      var cells = [
        asset.os,
        asset.arch,
        asset.filename,
        asset.sha256 || ""
      ];
      cells.forEach(function (value, i) {
        var td = document.createElement("td");
        if (i === 2) {
          var code = document.createElement("code");
          code.className = "il";
          code.textContent = "/releases/" + version + "/" + value;
          td.appendChild(code);
        } else if (i === 3) {
          var sha = document.createElement("code");
          sha.className = "il";
          sha.textContent = value;
          td.appendChild(sha);
        } else {
          td.textContent = String(value || "");
        }
        tr.appendChild(td);
      });
      tbody.appendChild(tr);
    });
  }

  function initLatestRelease() {
    var versionEls = document.querySelectorAll("[data-latest-version]");
    var dateEls = document.querySelectorAll("[data-latest-date]");
    var tableBody = document.querySelector("[data-latest-assets]");
    if (!versionEls.length && !dateEls.length && !tableBody) return;

    fetch("/releases/latest.json", { credentials: "omit" })
      .then(function (res) {
        if (!res.ok) throw new Error("bad status");
        return res.json();
      })
      .then(function (data) {
        if (!data || !data.version) return;
        var version = "v" + String(data.version).replace(/^v/, "");
        var current = versionEls.length ? versionEls[0].textContent.trim() : "";

        var dateText = "";
        if (data.published_at) {
          var d = new Date(data.published_at);
          if (!isNaN(d.getTime())) {
            dateText = d.toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric", timeZone: "UTC" });
          }
        }

        if (current !== version) {
          versionEls.forEach(function (el) { el.textContent = version; });
        }
        if (dateText) dateEls.forEach(function (el) { el.textContent = dateText; });
        fillAssetTable(tableBody, data);
      })
      .catch(function () {
        /* keep static text */
      });
  }

  document.addEventListener("DOMContentLoaded", function () {
    initThemeToggle();
    initTabs();
    initCopyButtons();
    initNavToggle();
    initDocsSidebar();
    initLatestRelease();
  });
})();
