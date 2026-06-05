// harb keyboard nav — minimal, no deps. Loaded from base.html.
//
// Global (every authenticated page):
//   ?      → toggle keyboard-help overlay
//   Esc    → close overlay
//   u      → up the hierarchy (entry view → parent feed; any other
//            authenticated page → home /ui/)
//   ← / u  → up the hierarchy (also: feed view → home)
//   N / n  → toggle "show unread only" filter (home + per-feed views)
//
// On any list view — home feeds (/ui/) and entry lists
// (/ui/feed, /ui/all, /ui/starred):
//   j / ↓  → focus next row
//   k / ↑  → focus previous row
//   Enter  → open the focused row's primary link
//   gg     → first row, G → last row
//
// Home master-detail (wide screens ≥ 64em only):
//   j / k  → move feed selection; the selected feed's entries are
//            previewed in the right-hand #feed-pane via htmx (?panel=1)
//   r      → mark ALL entries of the selected feed read (in place)
//   → / Enter → drill in to /ui/feed?id=… (full entries + article view)
//   ← / u  → back out to the feeds list (home)
//   On narrow screens the home view stays a plain feeds list and these
//   keys are inert (Enter follows the feed link as a full-page nav).
//
// Entry-list additions:
//   r      → toggle row's read state
//   s      → toggle row's star
//   R      → mark all read (if a "mark all read" button is present)
//
// On the entry view (/ui/entry):
//   r      → mark read/unread
//   s      → star/unstar
//   u      → back to parent feed
//
// Behaviour:
//   - The entry view auto-marks the entry as read after ~0.7 s of
//     dwell — both on the standalone entry page and on entries loaded
//     into the split-panel detail pane via htmx. In split mode the
//     matching list row is patched in place too (no scroll loss).
//     Navigating away or toggling read/star manually earlier cancels.
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
      try { localStorage.setItem("harb.theme", next); } catch (e) { /* ignore */ }
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
    if (e.key !== "u" && e.key !== "ArrowLeft") return;
    // In split-panel mode the entry detail lives inside #detail-pane;
    // that doesn't count as "on the entry view" — the universal u
    // should still walk us up to /ui/. Only bail when the article is
    // the page-level one (i.e. the standalone /ui/entry view).
    const article = $(".entry-full");
    if (article && !article.closest("#detail-pane")) return;
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
    // Track the focused row by its hash (the part of id="entry-<hash>"
    // after the prefix) in addition to its index. Index alone is
    // fragile across htmx swaps: outerHTML replacement of a list row
    // wipes the .kb-focus class, and if anything ever reorders rows
    // the index becomes wrong. Hash survives both.
    let focusedHash = "";
    const rowHash = (li) => {
      const id = li && li.id;
      return id && id.indexOf("entry-") === 0 ? id.slice("entry-".length) : "";
    };
    // Debounced "auto-preview the focused row into #detail-pane" so
    // holding j/k doesn't fire one htmx request per repeat. Only active
    // on entry-list pages (not the home feed-list) and only when the
    // viewport is wide enough that the split-panel is visible.
    const wideScreen = () => window.matchMedia("(min-width: 64em)").matches;
    // Open an entry row's title link. On wide screens we swap the entry
    // detail into the split-panel via htmx.ajax; on narrow screens we
    // follow the native href (full-page nav). This decision MUST live in
    // JS, not in an hx-trigger media-query filter on the anchor: htmx
    // calls preventDefault() on <a href> clicks *before* it evaluates
    // the trigger filter, so a filtered-out click on mobile cancelled
    // the native navigation AND fired no request — the tap was dead and
    // there was no way to open an article on mobile at all.
    const openEntry = (a) => {
      if (!a) return;
      const href = a.getAttribute("href");
      if (href && wideScreen() && window.htmx) {
        const sep = href.indexOf("?") >= 0 ? "&" : "?";
        try { htmx.ajax("GET", href + sep + "panel=1", "#detail-pane"); return; }
        catch (_) { /* fall through to full-page nav */ }
      }
      window.location.href = a.href;
    };
    let previewTimer = null;
    const schedulePreview = () => {
      if (!wideScreen()) return;
      // On entry-list pages we preview the focused entry into the
      // article pane (#detail-pane); on the home feed-list we preview
      // the focused feed's entry list into the master-detail pane
      // (#feed-pane). Both are gated on the target pane existing in the
      // DOM — it is display:none below 64em, so this is a no-op on
      // narrow screens and on any page without the pane.
      const paneSel = isEntryList ? "#detail-pane" : "#feed-pane";
      if (!document.querySelector(paneSel)) return;
      if (previewTimer !== null) clearTimeout(previewTimer);
      previewTimer = setTimeout(function () {
        previewTimer = null;
        if (idx < 0) return;
        const cur = rows()[idx];
        if (!cur) return;
        // Entry rows expose a.entry-link; home feed rows expose the
        // feed link as their first/primary anchor.
        const a = isEntryList ? cur.querySelector("a.entry-link") : cur.querySelector("a");
        if (!a || !window.htmx) return;
        const href = a.getAttribute("href");
        // htmx.ajax resolves the URL relative to the document, which
        // matches what a native click on the anchor would do. The
        // promise rejection on aborted requests is harmless here.
        const sep = href.indexOf("?") >= 0 ? "&" : "?";
        try { htmx.ajax("GET", href + sep + "panel=1", paneSel); } catch (_) { /* */ }
      }, 140);
    };
    // Mark every entry of the keyboard-selected feed read (home master-
    // detail only). We POST the existing feed-scope mark-all-read
    // endpoint directly — not via the .markall click interceptor — then
    // zero the row's unread count and refresh the preview pane in place,
    // so focus and scroll are preserved. Returns true when it acted.
    const markSelectedFeedRead = () => {
      if (isEntryList || !wideScreen() || idx < 0) return false;
      const row = rows()[idx];
      if (!row) return false;
      const a = row.querySelector("a");
      if (!a || !window.htmx) return false;
      let id;
      try { id = new URL(a.href).searchParams.get("id"); } catch (_) { return false; }
      if (!id) return false;
      fetch(uiURL("mark-all-read?scope=feed&id=" + encodeURIComponent(id)), {
        method: "POST", credentials: "same-origin",
      }).then(function (resp) {
        if (!resp.ok) return;
        const c = row.querySelector(".count");
        if (c) c.textContent = "0";
        const href = a.getAttribute("href");
        const sep = href.indexOf("?") >= 0 ? "&" : "?";
        try { htmx.ajax("GET", href + sep + "panel=1", "#feed-pane"); } catch (_) { /* */ }
      }).catch(function () { /* network hiccup — user can retry */ });
      return true;
    };
    const focusRow = (i) => {
      const all = rows();
      if (all.length === 0) return;
      if (i < 0) i = 0;
      if (i >= all.length) i = all.length - 1;
      all.forEach((r, j) => r.classList.toggle("kb-focus", j === i));
      idx = i;
      focusedHash = rowHash(all[i]);
      all[i].scrollIntoView({ block: "nearest" });
      schedulePreview();
    };
    // Re-apply kb-focus by hash whenever the list mutates. Handles
    // three cases that all wipe the class on the focused row:
    //   1. row read/star toggle returns a fresh <li> via outerHTML
    //   2. detail-pane read/star toggle issues an OOB row patch
    //   3. any future list-mutating swap
    // We also re-sync idx so j/k continue from the new position.
    const reapplyFocus = function () {
      if (!focusedHash) return;
      const all = rows();
      for (let i = 0; i < all.length; i++) {
        if (rowHash(all[i]) === focusedHash) {
          all[i].classList.add("kb-focus");
          idx = i;
          return;
        }
      }
    };
    // Mouse/tap click on a row → treat it as the new keyboard focus, so
    // subsequent j/k navigation continues from the clicked row. Event
    // delegation on the list lets the row's icon-buttons keep their own
    // click handlers; we track which row was hit and, for the title
    // link, drive the open ourselves (panel swap vs full-nav) — see
    // openEntry for why this can't be an hx-trigger media filter.
    list.addEventListener("click", function (e) {
      const li = e.target.closest("li");
      if (!li || li.classList.contains("empty") || !list.contains(li)) return;
      const all = rows();
      const i = all.indexOf(li);
      if (i < 0) return;
      all.forEach((r, j) => r.classList.toggle("kb-focus", j === i));
      idx = i;
      focusedHash = rowHash(li);
      const link = e.target.closest("a.entry-link");
      if (link && list.contains(link)) {
        // Honour modifier-clicks (cmd/ctrl/shift, middle-click) so
        // "open in new tab/window" keeps working on desktop.
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
        e.preventDefault();
        openEntry(link);
      }
    });
    const clickInRow = (sel) => {
      if (idx < 0) return;
      const btn = rows()[idx].querySelector(sel);
      if (btn) btn.click();
    };
    let lastG = 0;
    // After any htmx swap on this page (row toggle outerHTML, OOB
    // patches from detail-pane toggles, etc.) re-apply kb-focus to
    // the row whose hash matches the tracked focusedHash. Listen on
    // both afterSwap (synchronous, immediate) and afterSettle (covers
    // any settling-phase reflow) so the marker survives reliably.
    document.addEventListener("htmx:afterSwap", reapplyFocus);
    document.addEventListener("htmx:afterSettle", reapplyFocus);
    document.addEventListener("keydown", function (e) {
      if (inEditable(e) || helpOpen()) return;
      switch (e.key) {
        case "j": case "ArrowDown": focusRow(idx + 1); e.preventDefault(); break;
        case "k": case "ArrowUp":   focusRow(idx - 1); e.preventDefault(); break;
        case "ArrowRight":
          // Home master-detail: "drill in" to the selected feed's full
          // entries+article view. Equivalent to Enter on a feed row.
          if (!isEntryList && idx >= 0) {
            const a = rows()[idx].querySelector("a");
            if (a) { a.click(); e.preventDefault(); }
          }
          break;
        case "Enter":
          if (idx >= 0) {
            const row = rows()[idx];
            const link = row.querySelector("a.entry-link");
            if (link) {
              // Entry rows: openEntry picks panel-swap (wide) vs
              // full-page nav (narrow) itself.
              openEntry(link); e.preventDefault();
            } else {
              // Home feed-list rows: just follow the feed link.
              const a = row.querySelector("a");
              if (a) { a.click(); e.preventDefault(); }
            }
          }
          break;
        case "g":
          if (Date.now() - lastG < 500) { focusRow(0); lastG = 0; }
          else { lastG = Date.now(); }
          break;
        case "G": focusRow(rows().length - 1); break;
        case "r":
          // Entry list: toggle the focused row's read state. Home
          // master-detail: mark the whole selected feed read.
          if (isEntryList) clickInRow(".readbtn");
          else if (markSelectedFeedRead()) e.preventDefault();
          break;
        case "s": if (isEntryList) clickInRow(".starbtn"); break;
        case "R":
          if (isEntryList) {
            // Click the button (not form.submit()) so our click
            // interceptor below fires and history.back()s us to the
            // origin page, preserving its filter/tag state.
            const b = $(".markall");
            if (b) b.click();
          }
          break;
      }
    });
  }

  // ---- entry view --------------------------------------------------
  //
  // patchReadBtn updates a read-toggle button in place: the glyph
  // (●/○), the tooltip + aria-label, and the hx-post URL's state=
  // parameter so the next click toggles the correct direction. htmx
  // re-reads hx-post lazily on each event, so mutating the attribute
  // is enough.
  const patchReadBtn = function (btn, isRead) {
    if (!btn) return;
    btn.textContent = isRead ? "○" : "●";
    btn.title = isRead ? "mark unread" : "mark read";
    btn.setAttribute("aria-label", isRead ? "mark unread" : "mark read");
    const hxp = btn.getAttribute("hx-post");
    if (hxp) {
      btn.setAttribute("hx-post", hxp.replace(/state=\d/, "state=" + (isRead ? "0" : "1")));
    }
  };
  //
  // wireArticle attaches the dwell-based auto-mark-read timer and a
  // cancel hook to whatever .entry-full element is passed in. It is
  // called once at document load for the standalone /ui/entry page,
  // and again on every htmx:afterSwap into #detail-pane so entries
  // loaded into the split-panel pane get the same behaviour.
  let activeTimer = null;
  const wireArticle = function (article) {
    if (!article) return;
    if (activeTimer !== null) { clearTimeout(activeTimer); activeTimer = null; }
    if (article.classList.contains("read")) return;
    const m = article.id.match(/^entry-detail-(.+)$/);
    if (!m) return;
    const hash = m[1];
    const dwellMs = 700;
    activeTimer = setTimeout(function () {
      activeTimer = null;
      // Resolve via uiURL so this fetch works correctly under any
      // external URL prefix (e.g. /rss-test/ui/ on a tailscale
      // funnel). An absolute /ui/entry/read would silently 404 on a
      // prefixed deployment.
      fetch(uiURL("entry/read?id=" + encodeURIComponent(hash) + "&state=1"), {
        method: "POST",
        credentials: "same-origin",
      }).then(function (r) {
        if (!r.ok) return;
        // Patch the pane's article in place — re-fetching via htmx
        // would re-swap the entire pane and reset scroll position
        // (jarring at 0.7 s dwell), so we DOM-patch directly.
        article.classList.add("read");
        patchReadBtn(article.querySelector(".actions .readbtn"), true);
        // Also patch the matching list row so the left pane reflects
        // the new read state without a server round trip.
        const row = document.getElementById("entry-" + hash);
        if (row) {
          row.classList.add("read");
          patchReadBtn(row.querySelector(".readbtn"), true);
        }
      }).catch(function () { /* network hiccup — user can still click */ });
    }, dwellMs);
    const cancel = function () {
      if (activeTimer !== null) { clearTimeout(activeTimer); activeTimer = null; }
    };
    window.addEventListener("pagehide", cancel, { once: true });
    const actions = article.querySelector(".actions");
    if (actions) actions.addEventListener("click", cancel, true);
  };

  const article = $(".entry-full");
  if (article) {
    wireArticle(article);

    document.addEventListener("keydown", function (e) {
      if (inEditable(e) || helpOpen()) return;
      switch (e.key) {
        case "r": {
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
            // substring so we work regardless of any path prefix.
            const a = $(".meta a[href*='feed?id=']");
            if (a) window.location.href = a.href;
          }
          break;
        }
      }
    });
  }

  // ---- mark-all-read click interceptor -----------------------------
  //
  // The server's mark-all-read handler 303-redirects to a hardcoded
  // target ("./?unread=1" for feed scope; "all" for cross-feed). That
  // can't preserve whichever filter / tag / unread state the user had
  // on whichever page they came from before drilling in. Intercept the
  // click here, POST manually, then history.back() so the bfcache
  // reload (see "pageshow" handler above) shows the prior page with
  // fresh state. Falls back to the brand href when there is no useful
  // history (e.g. opened via bookmark, only entry in tab history).
  document.addEventListener("click", function (e) {
    const btn = e.target.closest(".markall");
    if (!btn) return;
    const form = btn.closest("form");
    if (!form) return;
    e.preventDefault();
    const action = form.getAttribute("action") || "";
    // Resolve action against the current document so a relative URL
    // works under any external prefix (e.g. /rss-test/ui/).
    const url = new URL(action, window.location.href).toString();
    btn.disabled = true;
    fetch(url, { method: "POST", credentials: "same-origin" })
      .then(function (r) {
        if (!r.ok) {
          btn.disabled = false;
          window.location.reload();
          return;
        }
        const ref = sameOriginRef();
        if (ref && window.history.length > 1) {
          window.history.back();
        } else {
          window.location.href = BASE;
        }
      })
      .catch(function () {
        btn.disabled = false;
        window.location.reload();
      });
  });

  // ---- htmx swap hooks --------------------------------------------
  //
  // When the split-panel swaps a new entry into #detail-pane, reset
  // the pane's scroll position and re-wire the dwell auto-mark-read
  // behaviour on the newly-inserted article. (kb-focus restoration on
  // list-row swaps is wired separately in the list-view block above,
  // listening directly to htmx:afterSwap + htmx:afterSettle.)
  document.addEventListener("htmx:afterSwap", function (e) {
    const target = e && e.target;
    if (!target) return;
    if (target.id === "detail-pane") {
      // New entry just swapped in — start at the top of the article.
      // Without this the pane keeps its previous scroll position so
      // long entries make the next entry look like it starts mid-body.
      target.scrollTop = 0;
      wireArticle(target.querySelector(".entry-full"));
    }
  });
})();

// ---- collapsible tag groups on the home page ---------------------
// Each .feed-group has a button.feed-group-toggle whose aria-controls
// points at a ul.feeds id. We persist per-tag collapse state in
// localStorage so the user's view sticks across navigations.
(function () {
  "use strict";
  var groups = document.querySelectorAll(".feed-group");
  if (!groups.length) return;
  var KEY = "harb.collapsedTags";
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


/* copy-to-clipboard buttons (e.g. RSS feed URL) */
(function () {
  document.addEventListener("click", function (ev) {
    var btn = ev.target.closest && ev.target.closest(".copy-url");
    if (!btn) return;
    ev.preventDefault();
    var sel = btn.getAttribute("data-copy-target");
    var el = sel && document.querySelector(sel);
    if (!el) return;
    var text = el.value != null ? el.value : el.textContent;
    function done() {
      var prev = btn.textContent;
      btn.textContent = "copied";
      btn.classList.add("copied");
      setTimeout(function () { btn.textContent = prev; btn.classList.remove("copied"); }, 1200);
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () { el.focus(); el.select(); });
    } else {
      el.focus(); el.select();
      try { document.execCommand("copy"); done(); } catch (e) {}
    }
  });
})();
