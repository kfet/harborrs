// harborrs keyboard nav — minimal, no deps. Loaded from base.html.
//
// Global (every authenticated page):
//   ?      → toggle keyboard-help overlay
//   Esc    → close overlay
//   u      → up the hierarchy (entry view → parent feed; any other
//            authenticated page → home /ui/)
//   N / n  → toggle "show unread only" filter (home + per-feed views)
//
// On any list view — home feeds (/ui/) and entry lists
// (/ui/feed, /ui/all, /ui/starred):
//   j / ↓  → focus next row
//   k / ↑  → focus previous row
//   Enter  → open the focused row's primary link
//   gg     → first row, G → last row
//
// Entry-list additions:
//   m      → toggle row's read button
//   s      → toggle row's star button
//   r      → run "mark all read" (if present)
//
// On the entry view (/ui/entry):
//   m      → mark read/unread
//   s      → star/unstar
//   u      → back to parent feed
//
// Behaviour:
//   - The entry view auto-marks the entry as read after ~2.5 s of
//     dwell. Navigating away earlier cancels.
//   - When a page is restored from the browser's back/forward cache
//     (e.g. you hit Back from an entry view), it is force-reloaded
//     so read/star toggles you made are reflected without F5.
(function () {
  "use strict";

  // ---- helpers -----------------------------------------------------
  const $  = (sel, root) => (root || document).querySelector(sel);
  const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

  // BASE is the relative prefix that, resolved against the current
  // page, points at the /ui/ root. The server emits it via
  // <html data-ui-base="..."> (see internal/ui/templates/base.html).
  // We never hard-code an absolute /ui path — under a path-prefix
  // deployment (e.g. Tailscale Funnel --set-path=/rss) the absolute
  // UI root is /rss/ui/, and an absolute /ui/... reference 404s.
  const BASE = document.documentElement.dataset.uiBase || "./";
  // Absolute pathname of the /ui/ root for this deployment, used to
  // tell whether a same-origin referrer is a list page.
  const UI_ROOT = new URL(BASE, window.location.href).pathname;
  // Build URLs relative to the /ui/ root. `seg` must be relative
  // (no leading slash).
  const uiURL = (seg) => BASE + seg;

  const inEditable = (e) => {
    const t = e.target;
    if (!t) return false;
    if (t.matches && t.matches("input, textarea, select")) return true;
    if (t.isContentEditable) return true;
    return false;
  };

  // ---- global: ? help overlay --------------------------------------
  const help = $("#kbd-help");
  const backdrop = $("#kbd-backdrop");
  const helpOpen = () => help && !help.hasAttribute("hidden");
  const toggleHelp = (show) => {
    if (!help) return;
    const open = show === undefined ? !helpOpen() : show;
    if (open) {
      help.removeAttribute("hidden");
      backdrop && backdrop.removeAttribute("hidden");
    } else {
      help.setAttribute("hidden", "");
      backdrop && backdrop.setAttribute("hidden", "");
    }
  };
  if (backdrop) backdrop.addEventListener("click", () => toggleHelp(false));

  // ---- theme toggle (auto → dark → light → auto) -------------------
  // Persisted client-side in localStorage so it survives reloads
  // without needing a server roundtrip. The inline <head> bootstrap
  // applies the stored value before paint to avoid theme flash.
  const themeBtn = $("#theme-toggle");
  if (themeBtn) {
    const cycle = ["auto", "dark", "light"];
    const glyph = { auto: "◐", dark: "●", light: "○", sepia: "◑" };
    const label = {
      auto:  "theme: auto (follows system) — click for dark",
      dark:  "theme: dark — click for light",
      light: "theme: light — click for auto",
      sepia: "theme: sepia — click for auto",
    };
    const current = () => document.documentElement.getAttribute("data-theme") || "auto";
    const paint = () => {
      const t = current();
      themeBtn.textContent = glyph[t] || "◐";
      themeBtn.title = label[t] || ("theme: " + t);
    };
    paint();
    themeBtn.addEventListener("click", function () {
      const t = current();
      const i = cycle.indexOf(t);
      const next = cycle[(i + 1) % cycle.length] || "auto";
      document.documentElement.setAttribute("data-theme", next);
      try { localStorage.setItem("harborrs.theme", next); } catch (e) { /* ignore */ }
      paint();
    });
  }

  // ---- refresh on back/forward bfcache restore ---------------------
  // Without this, hitting "back" from an entry view shows the previous
  // list snapshot — read/star toggles done on the entry don't appear
  // until you F5. Reload when the page is restored from bfcache.
  window.addEventListener("pageshow", function (e) {
    if (e.persisted) window.location.reload();
  });

  // ---- universal "u" — up the hierarchy ----------------------------
  // Entry view has its own `u` handler (back to parent feed) below.
  // On any other authenticated page that isn't already the home feed
  // list, `u` goes to /ui/. When we arrived from /ui/ (with whatever
  // filter the user had), prefer history.back() so the pill state and
  // scroll position are preserved.
  const sameOriginRef = function () {
    const r = document.referrer;
    if (!r) return null;
    try {
      const u = new URL(r);
      if (u.origin !== window.location.origin) return null;
      return u;
    } catch (_) { return null; }
  };
  document.addEventListener("keydown", function (e) {
    if (inEditable(e) || helpOpen()) return;
    if (e.key !== "u") return;
    if ($(".entry-full")) return;          // entry view handles its own `u`
    const path = window.location.pathname;
    if (path === UI_ROOT || path + "/" === UI_ROOT) return;  // already at top
    const ref = sameOriginRef();
    if (ref && (ref.pathname === UI_ROOT || ref.pathname + "/" === UI_ROOT)) {
      window.history.back();
    } else {
      window.location.href = BASE;
    }
    e.preventDefault();
  });

  // ---- N — toggle "show unread only" filter ------------------------
  // Wherever the page renders an `a.filter` pill (home and per-feed
  // entry list), pressing N navigates to the URL the pill points to.
  // No-op on pages without the pill (entry view, /ui/all, /ui/starred).
  document.addEventListener("keydown", function (e) {
    if (inEditable(e) || helpOpen()) return;
    if (e.key !== "N" && e.key !== "n") return;
    const pill = $("a.filter");
    if (!pill) return;
    window.location.href = pill.href;
    e.preventDefault();
  });

  document.addEventListener("keydown", function (e) {
    if (inEditable(e)) return;
    if (e.key === "?") { toggleHelp(); e.preventDefault(); return; }
    if (e.key === "Escape" && helpOpen()) { toggleHelp(false); e.preventDefault(); }
  }, true); // capture so help-toggle wins over page handlers

  // ---- list-view nav (works on home feeds list AND entry lists) ----
  //
  // We pick whichever list is visible: ul.entries (entry rows) takes
  // precedence; ul.feeds (home page) is the fallback. A "row" is just
  // an <li> with a primary <a> we can navigate to on Enter.
  const entryList = $("ul.entries");
  const feedLists = $$("ul.feeds");
  const list = entryList || (feedLists.length ? feedLists[0] : null);
  const isEntryList = !!entryList;

  if (list) {
    const rows = () => {
      if (isEntryList) {
        return $$("li", list).filter((li) => !li.classList.contains("empty"));
      }
      // Home page: collect rows across every visible ul.feeds (one per
      // tag group). Skip rows inside a hidden (collapsed) ul.
      const out = [];
      feedLists.forEach((ul) => {
        if (ul.hasAttribute("hidden")) return;
        $$("li", ul).forEach((li) => {
          if (!li.classList.contains("empty")) out.push(li);
        });
      });
      return out;
    };
    let idx = -1;
    const focusRow = (i) => {
      const all = rows();
      if (all.length === 0) return;
      if (i < 0) i = 0;
      if (i >= all.length) i = all.length - 1;
      all.forEach((r, j) => r.classList.toggle("kb-focus", j === i));
      idx = i;
      all[i].scrollIntoView({ block: "nearest" });
    };
    const clickInRow = (sel) => {
      if (idx < 0) return;
      const btn = rows()[idx].querySelector(sel);
      if (btn) btn.click();
    };
    let lastG = 0;
    document.addEventListener("keydown", function (e) {
      if (inEditable(e) || helpOpen()) return;
      switch (e.key) {
        case "j": case "ArrowDown": focusRow(idx + 1); e.preventDefault(); break;
        case "k": case "ArrowUp":   focusRow(idx - 1); e.preventDefault(); break;
        case "Enter":
          if (idx >= 0) {
            const a = rows()[idx].querySelector("a");
            if (a) { window.location.href = a.href; e.preventDefault(); }
          }
          break;
        case "g":
          if (Date.now() - lastG < 500) { focusRow(0); lastG = 0; }
          else { lastG = Date.now(); }
          break;
        case "G": focusRow(rows().length - 1); break;
        case "m": if (isEntryList) clickInRow(".readbtn"); break;
        case "s": if (isEntryList) clickInRow(".starbtn"); break;
        case "r":
          if (isEntryList) {
            const b = $(".markall");
            if (b) b.closest("form").submit();
          }
          break;
      }
    });
  }

  // ---- entry view --------------------------------------------------
  const article = $(".entry-full");
  if (article) {
    // Auto-mark-read after a short dwell time. Skip if already read,
    // skip if user navigates away first, skip if user toggles the
    // read state manually before the timer fires (otherwise we'd
    // race against an explicit "mark unread" click).
    if (!article.classList.contains("read")) {
      const m = article.id.match(/^entry-detail-(.+)$/);
      if (m) {
        const hash = m[1];
        const dwellMs = 2500;
        let timer = setTimeout(function () {
          timer = null;
          fetch(uiURL("entry/read") + "?id=" + encodeURIComponent(hash) + "&state=1", {
            method: "POST",
            credentials: "same-origin",
          }).then(function (r) {
            if (!r.ok) return;
            article.classList.add("read");
            const btn = $(".actions button:nth-of-type(1)");
            if (btn) btn.textContent = "mark unread";
          }).catch(function () { /* network hiccup — user can still click */ });
        }, dwellMs);
        const cancel = function () {
          if (timer !== null) { clearTimeout(timer); timer = null; }
        };
        // If the user navigates away or interacts with read/star
        // before the timer fires, don't mark.
        window.addEventListener("pagehide", cancel);
        const actions = $(".actions");
        if (actions) actions.addEventListener("click", cancel, true);
      }
    }

    document.addEventListener("keydown", function (e) {
      if (inEditable(e) || helpOpen()) return;
      switch (e.key) {
        case "m": {
          const b = $(".actions button:nth-of-type(1)");
          if (b) b.click();
          break;
        }
        case "s": {
          const b = $(".actions button:nth-of-type(2)");
          if (b) b.click();
          break;
        }
        case "u": {
          // Prefer going back via history when we came from a list
          // page on the same origin — that restores the unread-only
          // pill state, scroll position, and (via our bfcache reload)
          // shows the fresh read/star state. Fall back to the
          // canonical parent feed link in the meta line.
          const ref = sameOriginRef();
          // List-page pathnames are UI_ROOT (home) or its siblings
          // feed/all/starred under the same /ui/ root. Match by
          // stripping UI_ROOT off the referrer pathname.
          const isListPath = function (p) {
            if (p === UI_ROOT || p + "/" === UI_ROOT) return true;
            if (!p.startsWith(UI_ROOT)) return false;
            const tail = p.slice(UI_ROOT.length);
            return tail === "feed" || tail === "all" || tail === "starred";
          };
          if (ref && isListPath(ref.pathname)) {
            window.history.back();
          } else {
            // The .meta link to the parent feed is rendered as a
            // page-relative href like "feed?id=..." — match by a
            // substring so we work regardless of any path prefix
            // or whether the template later switches to absolute.
            const a = $(".meta a[href*='feed?id=']");
            if (a) window.location.href = a.href;
          }
          break;
        }
      }
    });
  }
})();

// ---- collapsible tag groups on the home page ---------------------
// Each .feed-group has a button.feed-group-toggle whose aria-controls
// points at a ul.feeds id. We persist per-tag collapse state in
// localStorage so the user's view sticks across navigations.
(function () {
  "use strict";
  var groups = document.querySelectorAll(".feed-group");
  if (!groups.length) return;
  var KEY = "harborrs.collapsedTags";
  var collapsed = {};
  try {
    var raw = localStorage.getItem(KEY);
    if (raw) collapsed = JSON.parse(raw) || {};
  } catch (e) { collapsed = {}; }
  function save() {
    try { localStorage.setItem(KEY, JSON.stringify(collapsed)); } catch (e) {}
  }
  groups.forEach(function (g) {
    var tag = g.dataset.tag || "";
    var btn = g.querySelector(".feed-group-toggle");
    var ul = g.querySelector("ul.feeds");
    if (!btn || !ul) return;
    function apply(isOpen) {
      btn.setAttribute("aria-expanded", isOpen ? "true" : "false");
      if (isOpen) ul.removeAttribute("hidden");
      else ul.setAttribute("hidden", "");
    }
    apply(!collapsed[tag]);
    btn.addEventListener("click", function () {
      var nowOpen = btn.getAttribute("aria-expanded") !== "true";
      apply(nowOpen);
      if (nowOpen) delete collapsed[tag]; else collapsed[tag] = true;
      save();
    });
  });
})();
