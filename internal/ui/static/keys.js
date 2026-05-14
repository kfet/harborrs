// harborrs keyboard nav — minimal, no deps. Loaded from base.html.
//
// Global:
//   ?      → toggle the keyboard-help overlay
//   Esc    → close the overlay
//
// On entry-list views (/ui/feed, /ui/all, /ui/starred):
//   j / ↓  → focus next entry row
//   k / ↑  → focus previous entry row
//   Enter  → open the focused entry
//   m      → click the row's "mark read/unread" button
//   s      → click the row's "star/unstar" button
//   r      → click "mark all read" if present
//   gg     → top, G → bottom
//
// On the entry view (/ui/entry):
//   j / l  → next entry in the same feed
//   k / h  → prev entry in the same feed
//   m      → mark read/unread
//   s      → star/unstar
//   u      → back to parent feed (the back-link in .meta)
(function () {
  "use strict";

  // ---- global: ? help overlay --------------------------------------
  const help = document.getElementById("kbd-help");
  const backdrop = document.getElementById("kbd-backdrop");
  const toggleHelp = (show) => {
    if (!help) return;
    const open = show === undefined ? help.hasAttribute("hidden") : show;
    if (open) { help.removeAttribute("hidden"); backdrop && backdrop.removeAttribute("hidden"); }
    else      { help.setAttribute("hidden", ""); backdrop && backdrop.setAttribute("hidden", ""); }
  };
  if (backdrop) backdrop.addEventListener("click", () => toggleHelp(false));
  document.addEventListener("keydown", function (e) {
    if (e.target.matches("input, textarea, select")) return;
    if (e.key === "?") { toggleHelp(); e.preventDefault(); return; }
    if (e.key === "Escape" && help && !help.hasAttribute("hidden")) {
      toggleHelp(false); e.preventDefault(); return;
    }
  });

  const onListPage = !!document.querySelector("ul.entries");
  const onEntryPage = !!document.querySelector(".entry-full");

  if (!onListPage && !onEntryPage) return;

  // ---- list page ----
  if (onListPage) {
    const rows = () => Array.from(document.querySelectorAll("li.entry"));
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
    const click = (sel) => {
      if (idx < 0) return;
      const btn = rows()[idx].querySelector(sel);
      if (btn) btn.click();
    };
    let lastG = 0;
    document.addEventListener("keydown", function (e) {
      if (e.target.matches("input, textarea, select")) return;
      if (help && !help.hasAttribute("hidden")) return; // help open → swallow
      switch (e.key) {
        case "j": case "ArrowDown": focusRow(idx + 1); e.preventDefault(); break;
        case "k": case "ArrowUp":   focusRow(idx - 1); e.preventDefault(); break;
        case "Enter":
          if (idx >= 0) {
            const a = rows()[idx].querySelector("a");
            if (a) window.location.href = a.href;
          }
          break;
        case "m": click(".readbtn"); break;
        case "s": click(".starbtn"); break;
        case "r": {
          const b = document.querySelector(".markall");
          if (b) b.closest("form").submit();
          break;
        }
        case "g":
          if (Date.now() - lastG < 500) { focusRow(0); lastG = 0; }
          else { lastG = Date.now(); }
          break;
        case "G": focusRow(rows().length - 1); break;
      }
    });
  }

  // ---- entry page: j/k navigate within the feed list this entry came from ----
  if (onEntryPage) {
    document.addEventListener("keydown", function (e) {
      if (e.target.matches("input, textarea, select")) return;
      if (help && !help.hasAttribute("hidden")) return;
      switch (e.key) {
        case "m": {
          const b = document.querySelector(".actions button:nth-of-type(1)");
          if (b) b.click();
          break;
        }
        case "s": {
          const b = document.querySelector(".actions button:nth-of-type(2)");
          if (b) b.click();
          break;
        }
        case "u": {
          const a = document.querySelector(".meta a[href^='/ui/feed?']");
          if (a) window.location.href = a.href;
          break;
        }
      }
    });
  }
})();
