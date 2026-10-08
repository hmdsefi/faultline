import { mountTimeline, mountError } from "./timeline/view.js";
import { nextTheme } from "./timeline/format.js";

// Entry module of timeline.html (ART-077). It is the only module that reads element IDs or touches
// the page outside #app: it owns the theme on <html> and the page-wide t key (UI-178), and it
// shows the error card when the data cannot be read.

const THEME_KEY = "faultline.theme";
const html = document.documentElement;
let view = null;

// Storage can be blocked (a private window, a strict file:// policy); the theme then lasts for
// the page only.
function storedTheme() {
  try {
    return window.localStorage.getItem(THEME_KEY) || "system";
  } catch {
    return "system";
  }
}

function applyTheme(t) {
  if (t === "light" || t === "dark") {
    html.setAttribute("data-theme", t);
  } else {
    html.removeAttribute("data-theme");
  }
}

const host = {
  theme() {
    return html.getAttribute("data-theme") || "system";
  },
  cycleTheme() {
    const t = nextTheme(host.theme());
    applyTheme(t);
    try {
      window.localStorage.setItem(THEME_KEY, t);
    } catch {
      // the theme still applies to this page
    }
    if (view !== null) {
      view.repaint();
    }
  },
};

applyTheme(storedTheme());
const root = document.getElementById("app");
let data = null;
try {
  data = JSON.parse(document.getElementById("faultline-data").textContent);
  view = mountTimeline(root, data, host);
} catch (err) {
  view = mountError(root, data, err, host);
}
// Caps Lock turns t into T.
document.addEventListener("keydown", (e) => {
  if ((e.key === "t" || e.key === "T") && !e.ctrlKey && !e.metaKey && !e.altKey && !e.defaultPrevented) {
    host.cycleTheme();
  }
});
window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
  if (view !== null) {
    view.repaint();
  }
});
