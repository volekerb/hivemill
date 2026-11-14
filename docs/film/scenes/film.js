// Shared timing helpers for the Hivemill commercial scenes.
// Contract (go/cmd/film-render): window.seek(t) is a pure function of t (seconds from the scene start);
// window.filmReady = true once fonts and images are loaded. Nothing here reads a clock or the network.
(function () {
  'use strict';
  const clamp = (x, a, b) => Math.min(b, Math.max(a, x));
  const lerp = (a, b, p) => a + (b - a) * p;
  const easeOut = p => 1 - Math.pow(1 - p, 3);
  const easeInOut = p => (p < 0.5 ? 4 * p * p * p : 1 - Math.pow(-2 * p + 2, 3) / 2);
  const easeBack = p => { const c = 1.4; return 1 + (c + 1) * Math.pow(p - 1, 3) + c * Math.pow(p - 1, 2); };
  const linear = p => p;
  // prog(t, a, b, ease): 0 before a, 1 after b, eased in between.
  const prog = (t, a, b, ease) => {
    const p = clamp((t - a) / (b - a), 0, 1);
    return (ease || easeOut)(p);
  };
  // Fade in over [a, a+d]; with out given, fade out again over [out, out+d].
  const fade = (t, a, d, out) => {
    let v = prog(t, a, a + d);
    if (out !== undefined) v *= 1 - prog(t, out, out + d);
    return v;
  };
  // Whole-pixel translate keeps text and screenshots sharp (no resampling between pixels).
  const tr = (x, y) => 'translate(' + Math.round(x) + 'px,' + Math.round(y) + 'px)';
  const $ = sel => document.querySelector(sel);
  const $$ = sel => Array.from(document.querySelectorAll(sel));

  const FONTS = ['400 20px Inter', '500 20px Inter', '600 20px Inter', '700 20px Inter',
    '400 20px "JetBrains Mono"', '700 20px "JetBrains Mono"'];

  async function ready() {
    await Promise.all(FONTS.map(f => document.fonts.load(f)));
    await document.fonts.ready;
    const missing = FONTS.filter(f => !document.fonts.check(f));
    if (missing.length) throw new Error('fonts not loaded: ' + missing.join(', '));
    await Promise.all($$('img').map(img => img.decode().catch(() => {
      throw new Error('image failed to load: ' + img.getAttribute('src'));
    })));
  }

  // boot(seekFn) installs window.seek and sets filmReady after fonts and images load. A failed asset leaves
  // filmReady unset (the renderer then stops with an error after its timeout) and puts the reason in
  // window.filmError.
  function boot(seekFn) {
    window.seek = t => { seekFn(Math.max(0, Number(t) || 0)); };
    const m = /(?:^#|&)t=([0-9.]+)/.exec(location.hash);  // preview aid: scene.html#t=2.5
    const t0 = m ? parseFloat(m[1]) : 0;
    window.seek(t0);
    ready().then(() => {
      window.seek(t0);
      window.filmReady = true;
    }, err => { window.filmError = String(err); });
  }

  window.Film = { clamp, lerp, easeOut, easeInOut, easeBack, linear, prog, fade, tr, $, $$, boot };
})();
