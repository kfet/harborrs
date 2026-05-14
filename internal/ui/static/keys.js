// harborrs keyboard nav — minimal, no deps. Loaded from base.html.
//
// Global (every authenticated page):
//   ?      → toggle keyboard-help overlay
//   Esc    → close overlay
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
(function () {
  "use strict";

  // ---- helpers -----------------------------------------------------
  const $  = (sel, root) => (root || document).querySelector(sel);
  const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

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
  const feedList  = $("ul.feeds");
  const list = entryList || feedList;
  const isEntryList = !!entryList;

  if (list) {
    const rows = () => $$("li", list).filter((li) => !li.classList.contains("empty"));
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
  if ($(".entry-full")) {
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
          const a = $(".meta a[href^='/ui/feed?']");
          if (a) window.location.href = a.href;
          break;
        }
      }
    });
  }
})();
