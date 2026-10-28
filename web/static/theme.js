/* Optional enhancements; every page is complete without them (theme then follows the OS, nav is plain links).
   1. Auto/Light/Dark segmented control in each [data-theme-slot]. 2. Keep scroll and open <details> across the 60 s meta refresh. */
(function () {
  var KEY = "factory-theme", KEEP = "factory-keep", root = document.documentElement, order = ["auto", "light", "dark"];
  var ICONS = {
    auto: '<circle cx="8" cy="8" r="5.5"/><path d="M8 2.5a5.5 5.5 0 0 1 0 11Z" fill="currentColor"/>',
    light: '<circle cx="8" cy="8" r="3"/><path d="M8 1.5v1.5M8 13v1.5M1.5 8H3M13 8h1.5M3.4 3.4l1 1M11.6 11.6l1 1M3.4 12.6l1-1M11.6 4.4l1-1"/>',
    dark: '<path d="M13 9.5A5.5 5.5 0 0 1 6.5 3a5.5 5.5 0 1 0 6.5 6.5Z"/>'
  };
  function get(store, k) { try { return window[store].getItem(k); } catch (e) { return null; } }
  function put(store, k, v) { try { window[store].setItem(k, v); } catch (e) { /* storage blocked: state lasts for this page only */ } }
  function apply(v) { if (v === "auto") root.removeAttribute("data-theme"); else root.setAttribute("data-theme", v); }
  var mode = get("localStorage", KEY);
  if (order.indexOf(mode) < 0) mode = "auto";
  apply(mode);
  function mark() {
    [].forEach.call(document.querySelectorAll(".seg button"), function (b) { b.setAttribute("aria-pressed", String(b.dataset.mode === mode)); });
  }
  [].forEach.call(document.querySelectorAll("[data-theme-slot]"), function (slot) {
    var seg = document.createElement("div");
    seg.className = "seg seg-fill"; seg.setAttribute("role", "group"); seg.setAttribute("aria-label", "Theme");
    order.forEach(function (m) {
      var b = document.createElement("button");
      b.type = "button"; b.dataset.mode = m; b.setAttribute("aria-label", m.charAt(0).toUpperCase() + m.slice(1));
      b.innerHTML = '<svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true">' + ICONS[m] + "</svg><span>" + m.charAt(0).toUpperCase() + m.slice(1) + "</span>";
      b.addEventListener("click", function () { mode = m; put("localStorage", KEY, m); apply(m); mark(); });
      seg.appendChild(b);
    });
    slot.appendChild(seg);
  });
  mark();
  var details = [].slice.call(document.querySelectorAll("details")), here = location.pathname + location.search;
  function did(d, i) { return d.id || "#" + i; }
  try {
    var kept = JSON.parse(get("sessionStorage", KEEP));
    if (kept && kept.path === here && Date.now() - kept.at < 90000 && Array.isArray(kept.open)) {
      details.forEach(function (d, i) { d.open = kept.open.indexOf(did(d, i)) >= 0; });
      window.scrollTo(0, kept.y || 0);
    }
  } catch (e) { /* unreadable or junk sessionStorage: keep the page defaults */ }
  window.addEventListener("pagehide", function () {
    put("sessionStorage", KEEP, JSON.stringify({ path: here, at: Date.now(), y: window.scrollY,
      open: details.map(did).filter(function (id, i) { return details[i].open; }) }));
  });
})();
