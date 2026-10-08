import { buildModel } from "./model.js";
import { formatCount, counted, shortTime } from "./format.js";
import { AXIS_HEIGHT, GUTTER, PALETTE, GLYPHS, namespace, isVisible, category, axisValue, axisExtent, timeValue, toX, laneHeight, gutterWidth, laneTop, laneIndex, fitView, zoomView, panView, centerOn, failureView, hitTest, sliceEdges, shownEdges, draw } from "./render.js";
import { h, icon, glyphIcon } from "./dom.js";
import { renderInspector, renderNarration, selectionText } from "./inspect.js";
import { themeButton, headerBar, titleRow, banner, label, panel, failurePanel, faultsPanel, reportCard, legendCard, errorState, emptyState } from "./panels.js";

// Run view (ART-077, ART-079): builds the page under root and wires the timeline. It touches only
// nodes under root, so several views can share one page; the theme belongs to the caller's host.

export const ZOOM_STEP = 1.25;
export const DRAG_SLOP = 3;
export const STAGE_MIN = 520;

// A wheel notch is WHEEL_PX of deltaY in pixel mode and WHEEL_LINES in line mode (Firefox); in page
// mode one page is one notch.
export const WHEEL_PX = 100;
export const WHEEL_LINES = 3;

// wheelNotches converts a wheel event's deltaY to notches by its deltaMode (0 pixels, 1 lines,
// 2 pages), so one notch zooms by ZOOM_STEP in every browser (ART-079 item 9).
export function wheelNotches(deltaY, deltaMode) {
  if (deltaMode === 1) {
    return deltaY / WHEEL_LINES;
  }
  if (deltaMode === 2) {
    return deltaY;
  }
  return deltaY / WHEEL_PX;
}

// NS_GLYPH is the glyph of each kind namespace's chip.
const NS_GLYPH = { assert: "assert", check: "check", disk: "disk", fault: "fault", history: "history", kernel: "boot", net: "send", run: "other" };

function shell(root, host) {
  const doc = root.ownerDocument;
  root.classList.add("fl-run");
  root.replaceChildren();
  const live = h(doc, "div", { class: "fl-vh", role: "status", "aria-live": "polite" });
  // The same words twice in a row are not a change, so a repeat gets a no-break space and is read
  // again.
  const announce = (text) => {
    live.textContent = live.textContent === text ? text + "\u00a0" : text;
  };
  return { doc, live, announce, theme: themeButton(doc, host) };
}

// mountTimeline renders data (a timeline data object, ART-060) under root and returns
// {select(seq), fit(), focusFailure(), repaint(), destroy()}. host, when given, owns the theme:
// {theme() → "system" | "light" | "dark", cycleTheme()}. It throws when the trace cannot be
// parsed; timeline-main.js then calls mountError.
export function mountTimeline(root, data, host) {
  const model = buildModel(data);
  const { doc, live, announce, theme } = shell(root, host);
  const win = doc.defaultView;
  const rep = data.report || {};
  const ok = model.records.length > 0;
  const hasFailure = ok && model.root !== 0 && model.bySeq.has(model.root);
  // Slice seqs that are not records are ignored (ART-079 item 10): only edited data has them.
  const members = [...model.slice].filter((seq) => model.bySeq.has(seq)).length;
  // allEdges are the slice's edges; state.edges those drawn under the filters (ART-079 item 10).
  const allEdges = sliceEdges(model);
  const state = {
    filters: { showEvents: false, hiddenNamespaces: new Set() },
    selected: -1,
    sliceOn: members > 0,
    edges: [],
    colors: {},
    fonts: { sans: "sans-serif", mono: "monospace" },
    failureNode: rep.failure && rep.failure.node_id ? rep.failure.node_id : 0,
    window: data.window || null,
  };
  state.edges = shownEdges(model, allEdges, state.filters);
  let view = { mode: "time", from: 0, to: 1, width: 800, height: 400, scrollY: 0, laneH: 40, gutter: GUTTER };

  // Timeline card: axis mode, causal slice, canvas and legend.
  const segTime = h(doc, "button", { type: "button", "aria-pressed": "true", text: "Time", disabled: !ok, onclick: () => setMode("time") });
  const segSeq = h(doc, "button", { type: "button", "aria-pressed": "false", text: "Sequence", disabled: !ok, onclick: () => setMode("seq") });
  let sliceTitle = null;
  if (members === 0) {
    sliceTitle = "This run has no causal slice";
  } else if (data.slice.truncated) {
    sliceTitle = "The slice stops at its cap of " + formatCount(data.slice.cap) + " records";
  }
  const sliceCount = members > 0 ? formatCount(members) + (data.slice.truncated ? "+" : "") : null;
  const sliceBtn = h(doc, "button", { class: "fl-toggle", type: "button", "aria-pressed": String(state.sliceOn), disabled: !ok || members === 0, title: sliceTitle, onclick: () => toggleSlice() },
    icon(doc, "slice"), "Causal slice", sliceCount !== null ? h(doc, "span", { class: "fl-count", text: sliceCount }) : null);
  const tlLabel = label(doc, "Timeline", ok ? counted(model.lanes.length, "lane") + ", " + counted(model.records.length, "record") : null);
  const tl = h(doc, "section", { class: "fl-tl fl-glass fl-smoked", "aria-label": "Timeline" },
    h(doc, "div", { class: "fl-tl-head" }, tlLabel, h(doc, "div", { class: "fl-seg", role: "group", "aria-label": "Axis" }, segTime, segSeq), sliceBtn));
  const wrap = h(doc, "div", { class: "fl-wrap" });
  const spacer = h(doc, "div", {});
  const canvas = h(doc, "canvas", { class: "fl-canvas", tabindex: "0", role: "application", "aria-roledescription": "timeline", "aria-label": "Run timeline, " + model.records.length + " records, " + model.lanes.length + " lanes" });
  const legend = h(doc, "ul", { class: "fl-legend", "aria-label": "Legend" });
  if (ok) {
    wrap.append(canvas, spacer);
    tl.append(wrap, legend);
  } else {
    tl.append(emptyState(doc));
  }

  // Control panel: the selection line, view buttons, the overview track and the kind chips.
  const narr = h(doc, "p", { class: "fl-narr" });
  // The keys work only while the timeline has focus, so the buttons name them in their titles,
  // not in aria-keyshortcuts.
  const round = (name, label, key, fn) => h(doc, "button", { class: "fl-round", type: "button", "aria-label": label, title: label + " (" + key + " on the timeline)", disabled: !ok, onclick: fn }, icon(doc, name));
  const jump = h(doc, "button", { class: "fl-primary", type: "button", "aria-label": "Jump to failure", title: hasFailure ? "Jump to failure (f on the timeline)" : "No failure in this run", disabled: !hasFailure, onclick: () => api.focusFailure() },
    icon(doc, "target"), h(doc, "span", { text: "Jump to failure" }));
  const overview = h(doc, "input", { class: "fl-overview", type: "range", min: "0", max: "1000", step: "1", value: "500", "aria-label": "View position", disabled: !ok });
  const shown = h(doc, "span", { class: "fl-shown fl-mono" });
  const kinds = h(doc, "div", { class: "fl-kinds", role: "group", "aria-label": "Kinds" }, h(doc, "span", { class: "fl-kl", "aria-hidden": "true", text: "Kinds" }));
  const ctl = h(doc, "section", { class: "fl-ctl fl-glass", "aria-label": "Timeline controls" }, narr,
    h(doc, "div", { class: "fl-row-ctl" }, round("fit", "Fit the whole run", "0", () => api.fit()), round("minus", "Zoom out", "-", () => zoomBy(1 / ZOOM_STEP)), round("plus", "Zoom in", "+", () => zoomBy(ZOOM_STEP)), jump, overview));
  if (ok) {
    buildKinds();
    ctl.append(kinds);
  }

  // Side panels and cards.
  const insp = h(doc, "div", { class: "fl-scroll" });
  const faults = faultsPanel(doc, model, (seq) => api.select(seq));
  const side = h(doc, "aside", { class: "fl-side", "aria-label": "Details" },
    failurePanel(doc, data, model, (seq) => api.select(seq)), faults.el, panel(doc, "Inspector", null, insp));
  const stage = h(doc, "div", { class: "fl-stage" }, tl, ctl, side);
  const page = h(doc, "main", { class: "fl-main", "aria-label": "Run view" }, titleRow(doc, data, announce));
  if (data.window) {
    page.append(banner(doc, data, model.records.length));
  }
  page.append(stage, h(doc, "div", { class: "fl-cards" }, reportCard(doc, data, model, announce), legendCard(doc)));
  const skip = ok ? h(doc, "button", { class: "fl-skip fl-primary", type: "button", text: "Skip to timeline", onclick: () => canvas.focus() }) : null;
  root.append(...[skip, headerBar(doc, rep, theme.el), page, live].filter((e) => e !== null));

  // The canvas paints every pixel opaque, so the browser need not blend it with the card below.
  const ctx = ok ? canvas.getContext("2d", { alpha: false }) : null;
  // A link or the close button in the Inspector is gone once the panel rebuilds; focus moves to
  // the new panel's heading or hint, so a keyboard user keeps their place (UI-175).
  let inspFocus = null;
  const actions = {
    select: (seq) => {
      api.select(seq);
      inspFocus.focus();
    },
    clear: () => {
      select(-1, false);
      inspFocus.focus();
    },
  };

  function buildKinds() {
    const counts = new Map();
    let events = 0;
    for (const r of model.records) {
      if (r.kind === "kernel.event" || r.kind === "kernel.defer") {
        events++;
      } else {
        const ns = namespace(r.kind);
        counts.set(ns, (counts.get(ns) || 0) + 1);
      }
    }
    const chip = (label, glyph, count, on, toggle) => {
      const b = h(doc, "button", { class: "fl-chip", type: "button", "aria-pressed": String(on) }, glyphIcon(doc, glyph, 13), label, h(doc, "span", { class: "fl-count", text: formatCount(count) }));
      b.addEventListener("click", () => {
        const now = b.getAttribute("aria-pressed") !== "true";
        b.setAttribute("aria-pressed", String(now));
        toggle(now);
        state.edges = shownEdges(model, allEdges, state.filters);
        refreshKinds();
        redraw();
      });
      kinds.append(b);
    };
    for (const ns of [...counts.keys()].sort()) {
      // Namespaces come from the trace: "constructor" must not find Object.prototype's.
      chip(ns, Object.hasOwn(NS_GLYPH, ns) ? NS_GLYPH[ns] : "other", counts.get(ns), true, (on) => {
        if (on) {
          state.filters.hiddenNamespaces.delete(ns);
        } else {
          state.filters.hiddenNamespaces.add(ns);
        }
      });
    }
    if (events > 0) {
      chip("scheduler events", "event", events, false, (on) => {
        state.filters.showEvents = on;
      });
    }
    kinds.append(shown);
  }

  function readPalette() {
    const style = win.getComputedStyle(root);
    for (const name of PALETTE) {
      state.colors[name] = style.getPropertyValue("--fl-" + name).trim() || "#888";
    }
    state.fonts = {
      sans: style.getPropertyValue("--fl-font").trim() || "sans-serif",
      mono: style.getPropertyValue("--fl-mono").trim() || "monospace",
    };
  }

  function renderLegend() {
    const present = new Set();
    for (const r of model.records) {
      if (isVisible(r, state.filters)) {
        present.add(category(r, model.root));
      }
    }
    legend.replaceChildren();
    for (const c of Object.keys(GLYPHS)) {
      if (present.has(c)) {
        legend.append(h(doc, "li", { class: "fl-pill" }, glyphIcon(doc, c, 11), GLYPHS[c].label));
      }
    }
  }

  function refreshKinds() {
    let n = 0;
    for (const r of model.records) {
      if (isVisible(r, state.filters)) {
        n++;
      }
    }
    shown.textContent = formatCount(n) + " of " + formatCount(model.records.length) + " shown";
    renderLegend();
  }

  // updateOverview paints the overview track: the whole run, fault spans, the failure tick and the
  // current view, and says which part is in view.
  function updateOverview() {
    const [a, b] = axisExtent(model, view.mode);
    const pct = (v) => (Math.max(0, Math.min(1, (v - a) / (b - a))) * 100).toFixed(2) + "%";
    const seg = (v0, v1, color) => "linear-gradient(90deg, transparent " + pct(v0) + ", " + color + " " + pct(v0) + ", " + color + " " + pct(v1) + ", transparent " + pct(v1) + ")";
    const layers = [];
    if (hasFailure) {
      const v = axisValue(model, view, model.bySeq.get(model.root));
      layers.push(seg(v - (b - a) * 0.006, v, "var(--fl-violation)"));
    }
    for (const band of model.bands) {
      if (band.type === "fault") {
        const v0 = timeValue(model, view.mode, band.from);
        layers.push(seg(v0, Math.max(timeValue(model, view.mode, band.to), v0 + (b - a) * 0.004), "color-mix(in srgb, var(--fl-fault) 75%, transparent)"));
      }
    }
    layers.push(seg(view.from, view.to, "var(--fl-ov-view)"), "var(--fl-line)");
    overview.style.setProperty("--fl-ov", layers.join(", "));
    overview.value = String(Math.round((((view.from + view.to) / 2 - a) / (b - a)) * 1000));
    const where = (v) => {
      if (view.mode === "seq") {
        return "#" + model.records[Math.max(0, Math.min(model.records.length - 1, Math.round(v)))].seq;
      }
      return shortTime(Math.max(a, Math.min(b, v)));
    };
    overview.setAttribute("aria-valuetext", "Showing " + where(view.from) + " to " + where(view.to));
  }

  function redraw() {
    if (!ok) {
      return;
    }
    draw(ctx, model, view, state);
    updateOverview();
  }

  // follow keeps the canvas the size of its well, which changes when the rows around it do (a
  // wrapped selection line or legend), not only with the window. A ResizeObserver calls it after
  // layout and before paint; it is not a timer.
  function follow() {
    if (Math.max(240, wrap.clientWidth) !== view.width || Math.max(AXIS_HEIGHT + 32, wrap.clientHeight) !== view.height) {
      resize();
      redraw();
    }
  }

  // resize sizes the canvas to its well: lanes share the height (LANE_MIN to LANE_MAX px each)
  // and the spacer lets the well scroll the rest.
  function resize() {
    const dpr = win.devicePixelRatio || 1;
    const width = Math.max(240, wrap.clientWidth);
    const height = Math.max(AXIS_HEIGHT + 32, wrap.clientHeight);
    const laneH = laneHeight(height, model.lanes.length);
    spacer.style.height = Math.max(0, AXIS_HEIGHT + model.lanes.length * laneH - height) + "px";
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    canvas.style.width = width + "px";
    canvas.style.height = height + "px";
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    view = { ...view, width, height, laneH, gutter: gutterWidth(width) };
  }

  // layout lets the stage fill the window below the title, so the timeline gets the full height.
  function layout() {
    const top = stage.getBoundingClientRect().top + win.scrollY;
    stage.style.setProperty("--fl-stage-h", Math.max(STAGE_MIN, Math.floor(win.innerHeight - top - 16)) + "px");
    resize();
    redraw();
  }

  // revealLane scrolls the well so lane li is in view.
  function revealLane(li) {
    const top = laneTop(view, li);
    let y = view.scrollY;
    if (top < AXIS_HEIGHT) {
      y = li * view.laneH;
    } else if (top + view.laneH > view.height) {
      y = AXIS_HEIGHT + (li + 1) * view.laneH - view.height;
    }
    if (y !== view.scrollY) {
      wrap.scrollTop = y;
      view = { ...view, scrollY: y };
    }
  }

  // select selects record index i (-1 clears); reveal centers it, as links do (ART-079 item 8).
  function select(i, reveal) {
    state.selected = i;
    if (i >= 0) {
      if (reveal) {
        view = centerOn(model, view, axisValue(model, view, i));
      }
      revealLane(laneIndex(model).get(model.records[i].node) || 0);
      announce(selectionText(model, i));
    }
    inspFocus = renderInspector(insp, model, i, actions);
    renderNarration(narr, model, i, hasFailure);
    const seq = i >= 0 ? model.records[i].seq : 0;
    for (const f of faults.rows) {
      f.row.setAttribute("aria-current", String(f.seq === seq));
    }
    redraw();
  }

  function setMode(mode) {
    if (view.mode === mode) {
      return;
    }
    segTime.setAttribute("aria-pressed", String(mode === "time"));
    segSeq.setAttribute("aria-pressed", String(mode === "seq"));
    view = fitView(model, { ...view, mode });
    redraw();
  }

  function toggleSlice() {
    state.sliceOn = !state.sliceOn;
    sliceBtn.setAttribute("aria-pressed", String(state.sliceOn));
    redraw();
  }

  // zoomBy zooms around the selection when it is in view, else around the center.
  function zoomBy(factor) {
    let x = view.gutter + (view.width - view.gutter) / 2;
    if (state.selected >= 0) {
      const sx = toX(view, axisValue(model, view, state.selected));
      if (sx >= view.gutter && sx <= view.width) {
        x = sx;
      }
    }
    view = zoomView(model, view, x, factor);
    redraw();
  }

  const api = {
    select(seq) {
      const i = model.bySeq.get(seq);
      if (i !== undefined) {
        select(i, true);
      }
    },
    fit() {
      if (ok) {
        view = fitView(model, view);
        redraw();
      }
    },
    // focusFailure frames the failure with its causes (UI-106) and selects it.
    focusFailure() {
      const v = ok ? failureView(model, view) : null;
      if (v === null) {
        announce("No failure recorded");
        return;
      }
      view = v;
      select(model.bySeq.get(model.root), false);
    },
    repaint() {
      theme.paint();
      if (ok) {
        readPalette();
        redraw();
      }
    },
    destroy() {
      win.removeEventListener("resize", layout);
      if (watcher !== null) {
        watcher.disconnect();
      }
      root.replaceChildren();
      root.classList.remove("fl-run");
    },
  };

  let watcher = null;
  inspFocus = renderInspector(insp, model, -1, actions);
  if (!ok) {
    narr.replaceChildren(h(doc, "span", { class: "fl-hint", text: "No records, so there is nothing to select." }));
    return api;
  }

  let drag = null;
  canvas.addEventListener("wheel", (e) => {
    e.preventDefault();
    if (e.shiftKey) {
      // Shift scrolls the lanes; browsers report a Shift wheel as deltaY or as deltaX.
      wrap.scrollTop += (e.deltaY || e.deltaX) * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? wrap.clientHeight : 1);
      return;
    }
    view = zoomView(model, view, e.offsetX, Math.pow(ZOOM_STEP, -wheelNotches(e.deltaY, e.deltaMode)));
    redraw();
  }, { passive: false });
  canvas.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) {
      return;
    }
    drag = { x: e.offsetX, moved: 0 };
    if (canvas.setPointerCapture) {
      canvas.setPointerCapture(e.pointerId);
    }
  });
  canvas.addEventListener("pointermove", (e) => {
    if (drag === null) {
      return;
    }
    const dx = e.offsetX - drag.x;
    drag.moved += Math.abs(dx);
    drag.x = e.offsetX;
    if (drag.moved > DRAG_SLOP) {
      view = panView(model, view, dx);
      redraw();
    }
  });
  canvas.addEventListener("pointerup", (e) => {
    if (drag !== null && drag.moved <= DRAG_SLOP) {
      select(hitTest(model, view, state.filters, e.offsetX, e.offsetY), false);
    }
    drag = null;
  });
  // A cancelled gesture (a touch the browser takes over, an alert) ends the drag; otherwise the
  // next move without a button would pan.
  for (const type of ["pointercancel", "lostpointercapture"]) {
    canvas.addEventListener(type, () => {
      drag = null;
    });
  }
  canvas.addEventListener("keydown", (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) {
      return;
    }
    const tenth = (view.width - view.gutter) / 10;
    // Caps Lock turns f into F.
    switch (e.key.length === 1 ? e.key.toLowerCase() : e.key) {
      case "+":
      case "=":
        zoomBy(ZOOM_STEP);
        break;
      case "-":
        zoomBy(1 / ZOOM_STEP);
        break;
      case "ArrowLeft":
        view = panView(model, view, tenth);
        redraw();
        break;
      case "ArrowRight":
        view = panView(model, view, -tenth);
        redraw();
        break;
      case "0":
        api.fit();
        break;
      case "f":
        api.focusFailure();
        break;
      default:
        return;
    }
    e.preventDefault();
  });
  overview.addEventListener("input", () => {
    const [a, b] = axisExtent(model, view.mode);
    view = centerOn(model, view, a + ((b - a) * Number(overview.value)) / 1000);
    redraw();
  });
  wrap.addEventListener("scroll", () => {
    view = { ...view, scrollY: wrap.scrollTop };
    redraw();
  });

  readPalette();
  renderNarration(narr, model, -1, hasFailure);
  refreshKinds();
  view = fitView(model, view);
  layout();
  // Listen only once the first layout has drawn: a draw error must not leave listeners on
  // detached nodes.
  win.addEventListener("resize", layout);
  if (win.ResizeObserver) {
    watcher = new win.ResizeObserver(follow);
    watcher.observe(wrap);
  }
  return api;
}

// mountError renders the page of timeline data that could not be read: the report's header and
// facts when data holds a report object, and the error card with the faultline render command.
// A report whose fields have the wrong types cannot stop the card: the page is then built
// without it.
export function mountError(root, data, err, host) {
  const rep = data !== null && typeof data === "object" && data.report !== null && typeof data.report === "object" ? data.report : null;
  try {
    return errorPage(root, data, rep, err, host);
  } catch {
    return errorPage(root, null, null, err, host);
  }
}

function errorPage(root, data, rep, err, host) {
  const { doc, live, announce, theme } = shell(root, host);
  const off = (text, pressed) => h(doc, "button", { type: "button", "aria-pressed": pressed, text, disabled: true });
  const head = h(doc, "div", { class: "fl-tl-head" }, label(doc, "Timeline", null),
    h(doc, "div", { class: "fl-seg", role: "group", "aria-label": "Axis" }, off("Time", "true"), off("Sequence", "false")),
    h(doc, "button", { class: "fl-toggle", type: "button", "aria-pressed": "false", disabled: true }, icon(doc, "slice"), "Causal slice"));
  const tl = h(doc, "section", { class: "fl-tl fl-glass fl-smoked", "aria-label": "Timeline" }, head, errorState(doc, rep, err, announce));
  const stage = h(doc, "div", { class: "fl-stage" }, tl);
  const page = h(doc, "main", { class: "fl-main", "aria-label": "Run view" });
  if (rep !== null) {
    stage.append(h(doc, "aside", { class: "fl-side", "aria-label": "Details" }, failurePanel(doc, data, null, () => {})));
    page.append(titleRow(doc, data, announce), stage, h(doc, "div", { class: "fl-cards" }, reportCard(doc, data, null, announce)));
  } else {
    page.append(stage);
  }
  root.append(headerBar(doc, rep || {}, theme.el), page, live);
  return {
    select() {},
    fit() {},
    focusFailure() {},
    repaint() {
      theme.paint();
    },
    destroy() {
      root.replaceChildren();
      root.classList.remove("fl-run");
    },
  };
}
