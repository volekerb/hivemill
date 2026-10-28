/* Seek API shared by every demo scene (plain JS, no dependencies, works from file://).
 *
 *   window.demo = {duration, seek(t), on(fn), brand(key), data}
 *
 * Data source (decision): the scene embeds its own data as JSON, so nothing is fetched and file:// works in a
 * headless browser:  <script type="application/json" id="demo-data">{"scene","duration","captions":[...],"brand":{...}}</script>
 * "captions" is the scene's list from storyboard.json and "brand" is brand.json (a build step or the author copies
 * them in). If that element is absent, demo.js falls back to a synchronous XMLHttpRequest of ../storyboard.json
 * and ../brand.json, which only works when the page is served over http (file:// blocks it); the scene id comes
 * from <body data-scene="...">.
 *
 * seek(t): pauses every document animation and sets its currentTime to t*1000 ms, runs the hooks registered with
 * demo.on(fn) as fn(t), and shows the caption whose [at, until) contains t in the .caption-bar element (created
 * when missing). brand(key) returns the value and fills every element with data-brand="<key>".
 */
(function () {
  "use strict";
  var hooks = [];
  var data = readData();
  var bar = null;
  var captionsOn = !/[?&]nocaptions=1(&|$)/.test(window.location.search);

  function xhrJson(path) {
    var x = new XMLHttpRequest();
    x.open("GET", path, false);
    x.send();
    if (x.status !== 0 && x.status !== 200) throw new Error("demo.js: cannot read " + path + " (HTTP " + x.status + ")");
    return JSON.parse(x.responseText);
  }

  function readData() {
    var el = document.getElementById("demo-data");
    if (el) return JSON.parse(el.textContent);
    var id = document.body.getAttribute("data-scene");
    var board = xhrJson("../storyboard.json");
    var scene = board.scenes.filter(function (s) { return s.id === id; })[0];
    if (!scene) throw new Error("demo.js: scene '" + id + "' is not in storyboard.json");
    return {scene: id, duration: scene.duration, captions: scene.captions, brand: xhrJson("../brand.json")};
  }

  function brand(key) {
    if (!(key in data.brand)) throw new Error("demo.js: unknown brand key '" + key + "'");
    var value = data.brand[key];
    var els = document.querySelectorAll('[data-brand="' + key + '"]');
    for (var i = 0; i < els.length; i++) {
      if (key === "wordmark_svg" && value.indexOf("<svg") === 0) els[i].innerHTML = value;
      else els[i].textContent = value;
    }
    return value;
  }

  function captionBar() {
    if (!bar) {
      bar = document.querySelector(".caption-bar");
      if (!bar) {
        bar = document.createElement("div");
        bar.className = "caption-bar";
        bar.appendChild(document.createElement("span"));
        (document.querySelector(".stage") || document.body).appendChild(bar);
      }
    }
    return bar;
  }

  function showCaption(t) {
    var text = null;
    for (var i = 0; i < data.captions.length; i++) {
      var c = data.captions[i];
      if (t >= c.at && t < c.until) { text = c.text; break; }
    }
    if (!captionsOn) text = null;
    var b = captionBar();
    b.firstElementChild.textContent = text === null ? "" : text;
    b.classList.toggle("on", text !== null);
  }

  var demo = {
    duration: data.duration,
    data: data,
    brand: brand,
    on: function (fn) { hooks.push(fn); },
    // captions(false) hides the caption bar (hero render); ?nocaptions=1 does the same from load. Takes effect at the next seek.
    captions: function (on) { captionsOn = on !== false; },
    seek: function (t) {
      var anims = document.getAnimations();
      for (var i = 0; i < anims.length; i++) {
        anims[i].pause();
        anims[i].currentTime = t * 1000;
      }
      for (var j = 0; j < hooks.length; j++) hooks[j](t);
      showCaption(t);
    }
  };
  window.demo = demo;
  Object.keys(data.brand).forEach(brand);
})();
