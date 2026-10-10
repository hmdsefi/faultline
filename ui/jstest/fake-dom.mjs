// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// A small DOM for the run view tests: elements, attributes, text, events and focus, a window with
// the APIs the view calls, and a canvas whose 2D context records what it draws. It implements only
// what ui/static/js uses.

class FakeText {
  constructor(text) {
    this.nodeType = 3;
    this.data = String(text);
    this.parentNode = null;
  }

  get textContent() {
    return this.data;
  }
}

class FakeStyle {
  setProperty(name, value) {
    this[name] = value;
  }

  removeProperty(name) {
    delete this[name];
  }
}

// RecordingContext records each paint operation with the state it ran under.
export class RecordingContext {
  constructor() {
    this.ops = [];
    this.path = [];
    this.stack = [];
    this.fillStyle = "#000";
    this.strokeStyle = "#000";
    this.globalAlpha = 1;
    this.lineWidth = 1;
    this.font = "10px sans-serif";
    this.textBaseline = "alphabetic";
    this.textAlign = "start";
    this.shadowColor = "transparent";
    this.shadowBlur = 0;
    this.dash = [];
  }

  record(op, extra) {
    this.ops.push({ op, fill: this.fillStyle, stroke: this.strokeStyle, alpha: this.globalAlpha, dash: this.dash.slice(), path: this.path.slice(), ...extra });
  }

  save() {
    this.stack.push({ fillStyle: this.fillStyle, strokeStyle: this.strokeStyle, globalAlpha: this.globalAlpha, lineWidth: this.lineWidth, font: this.font, dash: this.dash });
  }

  restore() {
    Object.assign(this, this.stack.pop());
  }

  setTransform() {}

  setLineDash(d) {
    this.dash = d.slice();
  }

  beginPath() {
    this.path = [];
  }

  moveTo(x, y) {
    this.path.push(["M", x, y]);
  }

  lineTo(x, y) {
    this.path.push(["L", x, y]);
  }

  arc(x, y, r) {
    this.path.push(["O", x, y, r]);
  }

  closePath() {
    this.path.push(["Z"]);
  }

  rect(x, y, w, h) {
    this.path.push(["R", x, y, w, h]);
  }

  roundRect(x, y, w, h) {
    this.path.push(["R", x, y, w, h]);
  }

  clip() {}

  clearRect() {}

  fill() {
    this.record("fill");
  }

  stroke() {
    this.record("stroke");
  }

  fillRect(x, y, w, h) {
    this.record("fillRect", { rect: [x, y, w, h] });
  }

  fillText(text, x, y) {
    this.record("fillText", { text, at: [x, y] });
  }

  measureText(text) {
    return { width: text.length * 6 };
  }
}

export class FakeElement {
  constructor(doc, tag, ns) {
    this.nodeType = 1;
    this.ownerDocument = doc;
    this.tagName = tag.toUpperCase();
    this.namespaceURI = ns;
    this.childNodes = [];
    this.parentNode = null;
    this.attrs = new Map();
    this.listeners = [];
    this.style = new FakeStyle();
    this.scrollTop = 0;
    this.clientWidth = doc.size.width;
    this.clientHeight = doc.size.height;
    this.width = 300;
    this.height = 150;
    this.value = "";
    this.context = null;
    const el = this;
    this.classList = {
      add: (c) => el.setAttribute("class", [...new Set([...el.classNames(), c])].join(" ")),
      remove: (c) => el.setAttribute("class", el.classNames().filter((x) => x !== c).join(" ")),
      contains: (c) => el.classNames().includes(c),
    };
  }

  classNames() {
    return (this.getAttribute("class") || "").split(" ").filter((c) => c !== "");
  }

  // A failing assertion prints elements this way; the default would walk the whole document
  // through ownerDocument and parentNode, which can take gigabytes.
  [Symbol.for("nodejs.util.inspect.custom")]() {
    return "<" + this.tagName.toLowerCase() + "> " + JSON.stringify(this.textContent.slice(0, 60));
  }

  get children() {
    return this.childNodes.filter((n) => n.nodeType === 1);
  }

  get childElementCount() {
    return this.children.length;
  }

  get textContent() {
    return this.childNodes.map((n) => n.textContent).join("");
  }

  set textContent(v) {
    this.replaceChildren(String(v));
  }

  append(...nodes) {
    for (const n of nodes) {
      // As in the DOM, a value that is not a node becomes text.
      const node = n !== null && typeof n === "object" ? n : new FakeText(n);
      if (node.parentNode) {
        node.parentNode.childNodes = node.parentNode.childNodes.filter((x) => x !== node);
      }
      node.parentNode = this;
      this.childNodes.push(node);
    }
  }

  replaceChildren(...nodes) {
    for (const n of this.childNodes) {
      n.parentNode = null;
    }
    this.childNodes = [];
    this.append(...nodes);
  }

  setAttribute(k, v) {
    this.attrs.set(k, String(v));
    if (k === "value") {
      this.value = String(v);
    }
  }

  getAttribute(k) {
    return this.attrs.has(k) ? this.attrs.get(k) : null;
  }

  hasAttribute(k) {
    return this.attrs.has(k);
  }

  removeAttribute(k) {
    this.attrs.delete(k);
  }

  addEventListener(type, fn) {
    this.listeners.push({ type, fn });
  }

  removeEventListener(type, fn) {
    this.listeners = this.listeners.filter((l) => l.type !== type || l.fn !== fn);
  }

  // dispatch runs the listeners of type with an event carrying props, and returns the event.
  dispatch(type, props) {
    const e = { type, target: this, defaultPrevented: false, preventDefault() {
      this.defaultPrevented = true;
    }, ...props };
    for (const l of this.listeners.slice()) {
      if (l.type === type) {
        l.fn(e);
      }
    }
    return e;
  }

  click() {
    if (!this.hasAttribute("disabled")) {
      this.dispatch("click", {});
    }
  }

  focus() {
    this.ownerDocument.activeElement = this;
  }

  setPointerCapture() {}

  getBoundingClientRect() {
    return { top: 0, left: 0, width: this.clientWidth, height: this.clientHeight };
  }

  getContext() {
    if (this.context === null) {
      this.context = new RecordingContext();
    }
    return this.context;
  }
}

// FakeDocument holds a documentElement with a body. options.size is the {width, height} every
// element reports as its client size, options.innerHeight the window's height (default 900);
// options.clipboard replaces navigator.clipboard; options.style maps custom properties to the
// values getComputedStyle reports (any other property reads ""). The window has a
// ResizeObserver that resized() runs and a matchMedia whose listeners mediaChanged() runs.
export class FakeDocument {
  constructor(options) {
    const opts = options || {};
    const style = opts.style || {};
    this.size = opts.size || { width: 1000, height: 400 };
    this.activeElement = null;
    this.listeners = [];
    this.selection = [];
    this.observers = [];
    this.media = [];
    const storage = new Map();
    const doc = this;
    this.defaultView = {
      devicePixelRatio: 1,
      innerHeight: opts.innerHeight || 900,
      scrollY: 0,
      listeners: [],
      navigator: { clipboard: opts.clipboard === undefined ? null : opts.clipboard },
      localStorage: {
        getItem: (k) => (storage.has(k) ? storage.get(k) : null),
        setItem: (k, v) => storage.set(k, String(v)),
      },
      getComputedStyle: () => ({ getPropertyValue: (name) => (Object.hasOwn(style, name) ? style[name] : "") }),
      getSelection: () => ({
        removeAllRanges: () => {
          doc.selection = [];
        },
        addRange: (r) => doc.selection.push(r),
      }),
      matchMedia: () => ({ matches: false, addEventListener: (type, fn) => doc.media.push({ type, fn }) }),
      ResizeObserver: class {
        constructor(fn) {
          this.fn = fn;
          this.targets = [];
        }

        observe(el) {
          this.targets.push(el);
          doc.observers.push(this);
        }

        disconnect() {
          this.targets = [];
          doc.observers = doc.observers.filter((o) => o !== this);
        }
      },
      addEventListener(type, fn) {
        this.listeners.push({ type, fn });
      },
      removeEventListener(type, fn) {
        this.listeners = this.listeners.filter((l) => l.type !== type || l.fn !== fn);
      },
    };
    this.documentElement = this.createElement("html");
    this.body = this.createElement("body");
    this.documentElement.append(this.body);
  }

  createElement(tag) {
    return new FakeElement(this, tag, "xhtml");
  }

  createElementNS(ns, tag) {
    return new FakeElement(this, tag, ns);
  }

  createRange() {
    return {
      node: null,
      selectNodeContents(n) {
        this.node = n;
      },
    };
  }

  getElementById(id) {
    return findAll(this.documentElement, (e) => e.getAttribute("id") === id)[0] || null;
  }

  addEventListener(type, fn) {
    this.listeners.push({ type, fn });
  }

  // resized runs the ResizeObservers, as a browser does after a layout that changed a size.
  resized() {
    for (const o of this.observers.slice()) {
      o.fn(o.targets.map((target) => ({ target })), o);
    }
  }

  // mediaChanged runs the matchMedia change listeners, as a change of color scheme does.
  mediaChanged() {
    for (const m of this.media.slice()) {
      if (m.type === "change") {
        m.fn({ matches: true });
      }
    }
  }

  // dispatch runs the document's listeners of type with an event carrying props.
  dispatch(type, props) {
    const e = { type, defaultPrevented: false, ...props };
    for (const l of this.listeners.slice()) {
      if (l.type === type) {
        l.fn(e);
      }
    }
    return e;
  }
}

// findAll returns the elements under root, root included, that match pred, in document order.
export function findAll(root, pred) {
  const out = [];
  const walk = (n) => {
    if (n.nodeType !== 1) {
      return;
    }
    if (pred(n)) {
      out.push(n);
    }
    for (const c of n.childNodes) {
      walk(c);
    }
  };
  walk(root);
  return out;
}

// byClass returns the elements under root with class c.
export function byClass(root, c) {
  return findAll(root, (e) => e.classNames().includes(c));
}

// byTag returns the elements under root with tag name tag.
export function byTag(root, tag) {
  return findAll(root, (e) => e.tagName === tag.toUpperCase());
}

// byText returns the first element with tag name tag whose text is text.
export function byText(root, tag, text) {
  return findAll(root, (e) => e.tagName === tag.toUpperCase() && e.textContent === text)[0];
}
