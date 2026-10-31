// strata web UI. Reads the JSON API under /v1; every view loads only what
// is on screen, so it stays fast at millions of images. Views are URLs:
//   #overview
//   #lineage            #lineage?focus=<row id>&stale=1
//   #search?q=<filter>&team=a&verdict=stale&page=2
//   #image?d=<digest>
//   #cleanup            #cleanup?scope=repo&where=<filter>
(function () {
  "use strict";

  var COL = { fresh: "#5fd39a", drifting: "#e8c84a", stale: "#f08a5d", abandoned: "#a3a3ad" };
  var V = ["fresh", "drifting", "stale", "abandoned"];
  var VIEWS = ["overview", "lineage", "search", "cleanup", "image"];
  var host = "";

  // ---------- helpers ----------
  function $(id) { return document.getElementById(id); }
  function esc(s) { return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) { return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]; }); }
  function fmt(n) { return Math.round(n || 0).toLocaleString("en-US"); }
  function short(n) { n = n || 0; return n >= 1e6 ? (n / 1e6).toFixed(2) + "M" : n >= 1e4 ? (n / 1e3).toFixed(0) + "k" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(n); }
  function bytes(n) { var u = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"], i = 0; n = n || 0; while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; } return n.toFixed(i > 1 ? 1 : 0) + " " + u[i]; }
  function nice(name) { name = name || ""; if (host && name.indexOf(host + "/") === 0) { name = name.slice(host.length + 1); } return name.replace(/^docker\.io\/library\//, "").replace(/@sha256:([0-9a-f]{12})[0-9a-f]{52}$/, "@$1"); }
  function ago(iso) {
    if (!iso) { return "—"; }
    var d = (Date.now() - new Date(iso).getTime()) / 864e5;
    if (d < 1 / 24) { return Math.max(1, Math.round(d * 1440)) + " min ago"; }
    if (d < 1) { return Math.round(d * 24) + " h ago"; }
    return Math.round(d) + "d ago";
  }
  function sum(vc) { return vc ? vc.fresh + vc.drifting + vc.stale + vc.abandoned : 0; }
  function mixHTML(vc) {
    var t = sum(vc) || 1;
    return V.map(function (v) { return vc && vc[v] ? '<i style="width:' + (100 * vc[v] / t).toFixed(2) + "%;background:" + COL[v] + '" title="' + v + " " + fmt(vc[v]) + '"></i>' : ""; }).join("");
  }
  function staleShare(vc) { var t = sum(vc); return t ? (vc.stale + vc.abandoned) / t : 0; }
  function shade(p) {
    function mix(a, b, t) { var pa = [1, 3, 5].map(function (i) { return parseInt(a.substr(i, 2), 16); }), pb = [1, 3, 5].map(function (i) { return parseInt(b.substr(i, 2), 16); }); return "rgb(" + pa.map(function (v, i) { return Math.round(v + (pb[i] - v) * t); }).join(",") + ")"; }
    return p < .5 ? mix("#5fd39a", "#e8c84a", p * 2) : mix("#e8c84a", "#f08a5d", Math.min(1, (p - .5) * 2));
  }
  function kindBadge(kind, drift) {
    if (kind === "external") { return '<span class="kind ext">external</span>'; }
    if (kind === "phantom") { return '<span class="kind ph" title="Layers several images share, from a base that is not in the indexed registry. List the base under external in strata.yaml to name it.">not in registry</span>'; }
    if (kind === "unrooted") { return '<span class="kind ph">no parent found</span>'; }
    if (kind === "base") { return drift ? '<span class="kind prev">' + drift + " build" + (drift > 1 ? "s" : "") + " behind</span>" : '<span class="kind">base</span>'; }
    return "";
  }
  function cmdHTML(text) { return '<div class="cmd"><span>' + esc(text) + '</span><button type="button" data-copy>Copy</button></div>'; }
  function toast(msg) { var t = $("toast"); t.textContent = msg; t.hidden = false; clearTimeout(toast.t); toast.t = setTimeout(function () { t.hidden = true; }, 6000); }
  function api(path, params) {
    var u = new URL(path, location.href);
    Object.keys(params || {}).forEach(function (k) {
      var v = params[k];
      if (Array.isArray(v)) { v.forEach(function (x) { u.searchParams.append(k, x); }); } else if (v !== undefined && v !== null && v !== "") { u.searchParams.set(k, v); }
    });
    return fetch(u, { headers: { Accept: "application/json" } }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (body) {
        if (!r.ok) { var e = new Error(body.message || ("HTTP " + r.status)); e.status = r.status; throw e; }
        return body;
      });
    });
  }
  function parseHash() {
    var h = location.hash.replace(/^#/, ""), i = h.indexOf("?");
    var view = (i < 0 ? h : h.slice(0, i)) || "overview";
    var params = new URLSearchParams(i < 0 ? "" : h.slice(i + 1));
    return { view: VIEWS.indexOf(view) < 0 ? "overview" : view, params: params };
  }
  function go(view, params) {
    var qs = params ? new URLSearchParams(params).toString() : "";
    location.hash = "#" + view + (qs ? "?" + qs : "");
  }
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-copy]");
    if (!b) { return; }
    var span = b.parentNode.querySelector("span");
    var done = function () { b.textContent = "Copied"; setTimeout(function () { b.textContent = "Copy"; }, 1200); };
    var fallback = function () { var r = document.createRange(); r.selectNodeContents(span); var s = getSelection(); s.removeAllRanges(); s.addRange(r); };
    try { navigator.clipboard.writeText(span.textContent).then(done, fallback); } catch (err) { fallback(); }
  });
  var tip = $("tip");
  function showTip(x, y, html) { tip.innerHTML = html; tip.hidden = false; tip.style.left = Math.min(x + 14, innerWidth - 360) + "px"; tip.style.top = Math.min(y + 16, innerHeight - tip.offsetHeight - 8) + "px"; }

  // ---------- overview ----------
  // Lineage, staleness and the registry map exist only once the index has
  // been refreshed. Before that, views say why they are empty and what
  // happens next, instead of looking broken.
  function notBuilt(st) { return st && !st.refreshed_at; }
  function whyEmpty(st) {
    if (st.refreshing) { return "strata is computing lineage and staleness for the first time. On a large index this takes several minutes; <b>Show latest</b> appears at the top when it is done."; }
    if (st.running) {
      var m = Math.round((st.refresh_every_seconds || 0) / 60);
      return "Lineage and staleness haven't been computed yet. The first refresh starts a minute into the crawl" + (m ? ", then every " + m + " min while it runs" : "") + ".";
    }
    return "Lineage and staleness haven't been computed for this index yet. Run <code>strata refresh</code>, or restart <code>strata serve</code> to compute them in the background.";
  }
  function pendingHTML(st) { return '<div class="pending" role="status"><b>Not computed yet.</b> ' + whyEmpty(st) + "</div>"; }

  function overview(el) {
    el.innerHTML = '<p class="loading">Loading the overview…</p>';
    Promise.all([api("v1/overview"), api("v1/map", { depth: 3 }), api("v1/status").catch(function () { return null; })]).then(function (r) {
      var o = r[0], map = r[1], pending = notBuilt(r[2]) && o.images > 0;
      host = o.host || "";
      $("foot").innerHTML = "Index<b>" + esc(o.host || "all registries") + "</b>" + (o.indexed_at ? "crawled " + ago(o.indexed_at) : "");
      var stale = o.images ? (o.verdicts.stale + o.verdicts.abandoned) / o.images : 0;
      var stacks = o.images ? Math.round(100 * (1 - o.stacks / o.images)) : 0;
      el.innerHTML =
        '<div class="head"><div><h1>' + esc(o.host || "Registry overview") + '</h1><p class="sub">What the index holds, how stale it is, and which bases the stale images sit on.</p></div>' + cmdHTML("strata summary --group-by root") + "</div>" +
        '<div class="kpis">' + [
          ["Images", fmt(o.images), fmt(o.tags) + " tags"],
          ["Repos", fmt(o.repos), ""],
          ["Distinct layer stacks", fmt(o.stacks), stacks + "% are retags"],
          ["Stored, deduplicated", bytes(o.dedup_bytes), bytes(o.apparent_bytes) + " as manifests state it"],
          ["Stale or abandoned", pending ? "—" : (100 * stale).toFixed(1) + "%", pending ? "not computed yet" : fmt(o.verdicts.stale + o.verdicts.abandoned) + " images"]
        ].map(function (k) { return '<div class="kpi"><div class="k">' + k[0] + '</div><div class="v num">' + k[1] + '</div><div class="d">' + k[2] + "</div></div>"; }).join("") + "</div>" +
        (pending ? pendingHTML(r[2]) : "") +
        '<div class="panel"><header><h2>Staleness mix</h2><span class="pct num">' + fmt(o.images) + ' images</span></header><div class="body"><div class="mix mixbig">' + mixHTML(o.verdicts) + '</div><div class="legend">' +
        V.map(function (v) { return '<span><i class="dot" style="background:' + COL[v] + '"></i>' + v + ' <b class="num" style="color:var(--cream);font-weight:600">' + fmt(o.verdicts[v]) + '</b> <span class="pct">' + (o.images ? (100 * o.verdicts[v] / o.images).toFixed(1) : "0") + "%</span></span>"; }).join("") + "</div></div></div>" +
        '<div class="grid2"><div class="panel"><header><h2>Where the stale images are</h2><span class="pct">stale or abandoned, by root base</span></header><div class="body"><div class="bars">' +
        (o.roots.length ? o.roots.map(function (r) {
          return '<div class="bar" data-focus="' + esc(r.id) + '" data-name="' + esc(r.name) + '" role="button" tabindex="0"><span class="n"><em>' + esc(nice(r.name)) + "</em>" + kindBadge(r.kind) + '</span><span class="c num">' + fmt(r.verdicts.stale + r.verdicts.abandoned) + " of " + short(r.images) + '</span><div class="mix">' + mixHTML(r.verdicts) + "</div></div>";
        }).join("") : '<p class="empty-state">' + (pending ? "Root bases appear once lineage is computed." : "No root bases yet. Run strata index.") + "</p>") + "</div></div></div>" +
        '<div class="panel"><header><h2>This week</h2>' + cmdHTML("strata diff --since 7d") + '</header><div class="body"><ul class="changes">' + changesHTML(o.changes) + "</ul></div></div></div>" +
        '<div class="panel"><header><h2>Registry map</h2><span class="pct">root bases left, descendants right · sized by images · colored by stale share · click to open in Lineage</span></header><div class="body ice"><div class="cols" id="icecols"></div><canvas id="ice" height="420" aria-label="Icicle chart of root bases and their descendants"></canvas></div></div>';
      if (!map.length) {
        $("icecols").innerHTML = "";
        $("ice").outerHTML = '<p class="empty-state">' + (pending ? "The registry map appears once lineage is computed." : "Nothing indexed yet. Run strata index.") + "</p>";
        return;
      }
      drawIce(map);
    }).catch(function (e) { el.innerHTML = '<p class="loading err">Could not load the overview: ' + esc(e.message) + "</p>"; });
  }
  function changesHTML(cs) {
    if (!cs || !cs.length) { return '<li><span class="when">—</span><span>No changes recorded in the last week. Snapshots start with the first index run.</span></li>'; }
    var words = { "new repo": "New repo", "removed repo": "Removed repo", "new root base": "New root base", "label coverage": "Label coverage", metric: "Changed" };
    return cs.slice(0, 8).map(function (c) {
      var what = c.metric ? " " + esc(c.metric.replace(/_/g, " ")) + ": " + fmt(c.from) + " → " + fmt(c.to) : "";
      return '<li><span class="when">' + esc(words[c.kind] || c.kind) + "</span><span><b>" + esc(nice(c.group)) + "</b>" + what + "</span></li>";
    }).join("");
  }
  document.addEventListener("click", function (e) { var b = e.target.closest("[data-focus]"); if (b) { focusOn(b.getAttribute("data-focus"), b.getAttribute("data-name")); } });
  document.addEventListener("keydown", function (e) { var b = e.target.closest && e.target.closest("[data-focus]"); if (b && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); focusOn(b.getAttribute("data-focus"), b.getAttribute("data-name")); } });

  var iceRects = [], iceMap = [];
  function drawIce(map) {
    if (map) { iceMap = map; }
    var c = $("ice");
    if (!c) { return; }
    var box = c.parentNode.clientWidth;
    if (!box) { return; }
    var kids = {}, roots = [], byId = {};
    iceMap.forEach(function (r) { byId[r.id] = r; if (r.parent) { (kids[r.parent] = kids[r.parent] || []).push(r); } else { roots.push(r); } });
    var depth = 1;
    iceMap.forEach(function (r) { depth = Math.max(depth, (r.parent ? r.depth : 0) + 1); });
    var H = innerWidth < 860 ? 360 : 420, dpr = devicePixelRatio || 1;
    c.width = box * dpr; c.height = H * dpr; c.style.height = H + "px";
    var g = c.getContext("2d"); g.setTransform(dpr, 0, 0, dpr, 0, 0); g.clearRect(0, 0, box, H);
    $("icecols").style.gridTemplateColumns = "repeat(" + depth + ", 1fr)";
    $("icecols").innerHTML = Array.from({ length: depth }, function (_, i) { return "<span>" + (i === 0 ? "Root bases" : "Generation " + i) + "</span>"; }).join("");
    iceRects = [];
    var total = roots.reduce(function (a, r) { return a + r.images; }, 0) || 1, col = box / depth;
    function place(n, d, y, h) {
      if (d >= depth || h < .6) { return; }
      var x = d * col, w = col - 3, hh = Math.max(h - 2, .6);
      g.fillStyle = "#17152a"; g.fillRect(x, y, w, hh);
      g.fillStyle = n.kind === "phantom" || n.kind === "unrooted" ? "#3d3860" : shade(staleShare(n.verdicts) * 1.6);
      g.fillRect(x, y, 3, hh);
      g.globalAlpha = .16; g.fillRect(x + 3, y, w - 3, hh); g.globalAlpha = 1;
      if (hh > 18 && w > 80) {
        g.fillStyle = "#f4f1ff"; g.font = "600 12px Inter, system-ui, sans-serif";
        var label = nice(n.name), full = label, max = w - 64;
        while (g.measureText(label).width > max && label.length > 4) { label = label.slice(0, -2); }
        if (label !== full) { label += "…"; }
        g.fillText(label, x + 10, y + Math.min(hh / 2 + 4, 16));
        g.fillStyle = "#8e8aa6"; g.font = "11px 'JetBrains Mono', monospace"; g.textAlign = "right";
        g.fillText(short(n.images), x + w - 8, y + Math.min(hh / 2 + 4, 16)); g.textAlign = "left";
      }
      iceRects.push({ n: n, x: x, y: y, w: w, h: hh });
      var ks = (kids[n.id] || []).slice().sort(function (a, b) { return b.images - a.images; });
      // Children are capped per parent, so they may not fill the block;
      // each keeps its true share of the parent's images.
      var shown = ks.reduce(function (a, k) { return a + k.images; }, 0), cy = y;
      ks.forEach(function (k) { var kh = h * k.images / Math.max(n.images, shown || 1); place(k, d + 1, cy, kh); cy += kh; });
    }
    var y = 0;
    roots.sort(function (a, b) { return b.images - a.images; }).forEach(function (r) { var h = H * r.images / total; place(r, 0, y, h); y += h; });
  }
  function iceHit(e) { var r = $("ice").getBoundingClientRect(), x = e.clientX - r.left, y = e.clientY - r.top; for (var i = iceRects.length - 1; i >= 0; i--) { var t = iceRects[i]; if (x >= t.x && x <= t.x + t.w && y >= t.y && y <= t.y + t.h) { return t.n; } } return null; }
  document.addEventListener("mousemove", function (e) {
    if (!e.target || e.target.id !== "ice") { return; }
    var n = iceHit(e);
    if (!n) { tip.hidden = true; return; }
    showTip(e.clientX, e.clientY, "<b>" + esc(nice(n.name)) + "</b>" + fmt(n.images) + " images · " + (100 * staleShare(n.verdicts)).toFixed(1) + '% stale or abandoned<div class="mix">' + mixHTML(n.verdicts) + "</div>");
  });
  document.addEventListener("mouseout", function (e) { if (e.target && e.target.id === "ice") { tip.hidden = true; } });
  document.addEventListener("click", function (e) {
    if (!e.target || e.target.id !== "ice") { return; }
    var n = iceHit(e);
    if (!n) { return; }
    tip.hidden = true;
    if (n.kind === "repo") { focusOn(n.parent, null); } else { focusOn(n.id, n.name); }
  });

  // ---------- lineage outline ----------
  // children[id] = { rows, total, loading, error }. Rows load one level
  // at a time and only the visible window is drawn.
  var L = { focus: "", focusName: "", stale: false, open: {}, children: {}, crumbs: [] };
  var ROW = 40, PAGE = 100, IMG_PAGE = 50;
  function focusOn(id, name) {
    var p = {}; if (id) { p.focus = id; } if (L.stale) { p.stale = "1"; }
    if (id && name) { L.names[id] = name; }
    go("lineage", p);
  }
  L.names = {};
  function load(parent) {
    var key = parent || "";
    var c = L.children[key] = L.children[key] || { rows: [], total: 0 };
    if (c.loading) { return; }
    c.loading = true; c.error = null;
    var isRepo = key.indexOf("repo|") === 0;
    api("v1/outline", { parent: key, only_stale: L.stale ? "true" : "", offset: c.rows.length, limit: isRepo ? IMG_PAGE : PAGE }).then(function (o) {
      c.rows = c.rows.concat(o.rows); c.total = o.total; c.loading = false;
      if (!key && $("exthint")) { $("exthint").hidden = !c.rows.some(function (r) { return r.kind === "phantom"; }); }
      o.rows.forEach(function (r) { if (r.kind !== "image") { L.names[r.id] = r.name; } });
      renderOutline();
    }).catch(function (e) { c.loading = false; c.error = e.message; renderOutline(); });
    renderOutline();
  }
  function flatten() {
    var rows = [];
    function walk(key, depth) {
      var c = L.children[key];
      if (!c) { return; }
      c.rows.forEach(function (r) {
        rows.push({ r: r, depth: depth });
        if (L.open[r.id] && r.has_children) { walk(r.id, depth + 1); }
      });
      if (c.loading) { for (var s = 0; s < 3; s++) { rows.push({ skel: true, depth: depth }); } }
      if (c.error) { rows.push({ error: c.error, depth: depth, key: key }); }
      if (!c.loading && c.rows.length < c.total) { rows.push({ more: key, depth: depth, shown: c.rows.length, total: c.total }); }
    }
    walk(L.focus, 0);
    return rows;
  }
  var rows = [];
  function lineageView(el, params) {
    var focus = params.get("focus") || "", stale = params.get("stale") === "1";
    if (focus !== L.focus || stale !== L.stale || !el.querySelector("#vp")) {
      if (focus !== L.focus || stale !== L.stale) { L.open = {}; L.children = {}; }
      L.focus = focus; L.stale = stale;
      el.innerHTML =
        '<div class="head"><div><h1>Lineage</h1><nav class="crumbs" id="crumbs" aria-label="Focus"></nav></div><span id="lincmd"></span></div>' +
        '<p class="pending" id="exthint" hidden>Some images are built on bases that are not in this registry, such as images from Docker Hub or public ECR, so their roots show as <b>not in registry</b>. ' +
        'List those bases under <code>external:</code> in strata.yaml (for example <code>docker.io/library/debian:bookworm-slim</code>) and they are fetched on the next index and named here.</p>' +
        '<div class="panel"><header><div class="toolbar"><div class="seg" role="group" aria-label="Show">' +
        '<button type="button" data-show="all" aria-pressed="' + !stale + '">All images</button><button type="button" data-show="stale" aria-pressed="' + stale + '">Only stale</button></div>' +
        '<div class="seg" role="group" aria-label="Expand"><button type="button" id="collapse">Collapse all</button></div></div><span class="pct num" id="rowsinfo"></span></header>' +
        '<div class="outline"><div class="otable"><div class="ohead"><span>Base, repo or image</span><span class="r">Images</span><span>Fresh · drifting · stale · abandoned</span><span class="r">Stale</span><span class="r">Drift</span><span class="r">Newest</span></div>' +
        '<div class="vp" id="vp" role="tree" aria-label="Lineage outline"><div id="spacer"></div></div></div></div></div>';
      $("vp").addEventListener("scroll", function () { requestAnimationFrame(paint); });
    }
    if (!L.children[L.focus]) { load(L.focus); } else { renderOutline(); }
  }
  function renderOutline() {
    var spacer = $("spacer");
    if (!spacer) { return; }
    rows = flatten();
    spacer.style.height = rows.length * ROW + "px";
    paint();
    var top = L.children[L.focus];
    $("rowsinfo").textContent = top ? fmt(rows.length) + " rows shown" + (top.total ? " · " + fmt(top.total) + " at the top level" : "") : "";
    var crumbs = '<button type="button" class="linkish" data-crumb="">All roots</button>';
    if (L.focus) { crumbs += ' / <span style="color:var(--cream)">' + esc(nice(L.names[L.focus] || L.focus.replace(/^repo\|[^|]*\|/, ""))) + "</span>"; }
    $("crumbs").innerHTML = crumbs;
    var name = L.names[L.focus] || "";
    $("lincmd").innerHTML = cmdHTML(L.focus ? (L.focus.indexOf("repo|") === 0 ? "strata query 'repo = \"" + L.focus.split("|")[2] + "\"'" : "strata descendants " + (name && name.indexOf(" ") < 0 ? name : L.focus) + " --depth 1") : "strata roots --count-descendants");
  }
  function paint() {
    var vp = $("vp"), spacer = $("spacer");
    if (!vp) { return; }
    var top = vp.scrollTop, h = vp.clientHeight, from = Math.max(0, Math.floor(top / ROW) - 8), to = Math.min(rows.length, Math.ceil((top + h) / ROW) + 8);
    var html = "";
    for (var i = from; i < to; i++) { html += rowHTML(rows[i], i); }
    if (!rows.length && L.children[L.focus] && !L.children[L.focus].loading) {
      html = notBuilt(lastStatus) ? '<div class="empty-state">' + pendingHTML(lastStatus) + "</div>" : '<div class="empty-state">' + (L.stale ? "Nothing stale here." : "Nothing indexed yet. Run strata index.") + "</div>";
    }
    spacer.innerHTML = html;
  }
  function indent(d) { var s = '<span class="indent">'; for (var i = 0; i < d; i++) { s += "<i></i>"; } return s + "</span>"; }
  var chev = '<svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><path d="M4.5 2.5 8 6l-3.5 3.5"/></svg>';
  function rowHTML(x, i) {
    var y = 'style="top:' + i * ROW + 'px"';
    if (x.skel) { return '<div class="orow" ' + y + '><div class="name">' + indent(x.depth) + '<span class="tw leaf"></span><div class="skel" style="width:' + (120 + (i * 37) % 160) + 'px"></div></div><span></span><div class="skel" style="width:100%"></div><span></span><span></span><span></span></div>'; }
    if (x.error) { return '<div class="more" ' + y + ">" + indent(x.depth) + '<span class="err">Could not load: ' + esc(x.error) + '</span><button type="button" data-retry="' + esc(x.key) + '">Retry</button></div>'; }
    if (x.more !== undefined) {
      var isRepo = x.more.indexOf("repo|") === 0;
      return '<div class="more" ' + y + ">" + indent(x.depth) + '<span class="pct num">' + fmt(x.shown) + " of " + fmt(x.total) + (isRepo ? " images" : " rows") + ' shown</span><button type="button" data-more="' + esc(x.more) + '">Load ' + (isRepo ? IMG_PAGE : PAGE) + " more</button>" +
        (isRepo ? '<button type="button" data-search-repo="' + esc(x.more.split("|")[2]) + '">Open in Search</button>' : "") + "</div>";
    }
    var r = x.r;
    if (r.kind === "image") {
      var v = r.verdict || "";
      return '<div class="orow" role="treeitem" aria-level="' + (x.depth + 1) + '" ' + y + '><div class="name">' + indent(x.depth) + '<span class="tw leaf"></span><i class="dot" style="background:' + (COL[v] || "#6b6880") + '"></i><span class="label"><a class="linkish" href="#image?d=' + encodeURIComponent(r.digest) + '">' + esc(nice(r.name)) + "</a></span>" + (r.tags && r.tags.length ? "" : '<span class="kind">untagged</span>') +
        '</div><span class="r cnt num">1</span><div class="mix" style="height:6px">' + (COL[v] ? '<i style="width:100%;background:' + COL[v] + '"></i>' : "") + '</div><span class="r pct">' + esc(v) + '</span><span class="r drift' + (r.drift ? " bad" : "") + '">' + (r.drift ? r.drift + " gen" : "—") + '</span><span class="r pct num">' + ago(r.newest) + "</span></div>";
    }
    var open = !!L.open[r.id], sp = 100 * staleShare(r.verdicts);
    var label = r.kind === "repo" ? '<span class="label">' + esc(nice(r.name)) + (r.team ? ' <span class="t">· ' + esc(r.team) + "</span>" : "") + "</span>"
      : r.kind === "unrooted" ? '<span class="label">' + esc(r.name) + "</span>"
        : '<span class="label"><button type="button" data-focus="' + esc(r.id) + '" data-name="' + esc(r.name) + '" title="Focus here">' + esc(nice(r.name)) + "</button></span>";
    return '<div class="orow" role="treeitem" aria-expanded="' + open + '" aria-level="' + (x.depth + 1) + '" ' + y + '><div class="name">' + indent(x.depth) +
      '<button type="button" class="tw' + (r.has_children ? "" : " leaf") + '" data-toggle="' + esc(r.id) + '" aria-expanded="' + open + '" aria-label="' + (open ? "Collapse " : "Expand ") + esc(nice(r.name)) + '">' + chev + "</button>" + label + kindBadge(r.kind, r.kind === "base" ? r.drift : 0) + "</div>" +
      '<span class="r cnt num">' + short(r.images) + '</span><div class="mix" style="height:8px">' + mixHTML(r.verdicts) + '</div><span class="r pct num' + (sp > 25 ? " hot" : "") + '">' + sp.toFixed(0) + '%</span><span class="r drift' + (r.drift ? " bad" : "") + '">' + (r.drift ? r.drift + " gen" : "—") + '</span><span class="r pct num">' + ago(r.newest) + "</span></div>";
  }
  document.addEventListener("click", function (e) {
    if (!$("vp") || !$("vp").contains(e.target)) { return; }
    var t = e.target.closest("[data-toggle]");
    if (t) {
      var id = t.getAttribute("data-toggle");
      if (L.open[id]) { delete L.open[id]; renderOutline(); return; }
      L.open[id] = true;
      if (!L.children[id]) { load(id); } else { renderOutline(); }
      return;
    }
    var m = e.target.closest("[data-more]");
    if (m) { load(m.getAttribute("data-more")); return; }
    var rt = e.target.closest("[data-retry]");
    if (rt) { load(rt.getAttribute("data-retry")); return; }
    var sr = e.target.closest("[data-search-repo]");
    if (sr) { go("search", { q: 'repo = "' + sr.getAttribute("data-search-repo") + '"' }); }
  });
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-show]");
    if (b) { var p = {}; if (L.focus) { p.focus = L.focus; } if (b.getAttribute("data-show") === "stale") { p.stale = "1"; } go("lineage", p); return; }
    if (e.target.id === "collapse") { L.open = {}; renderOutline(); return; }
    var c = e.target.closest("[data-crumb]");
    if (c) { focusOn(c.getAttribute("data-crumb"), null); }
  });

  // ---------- search ----------
  var FACET_PARAMS = { Verdict: "verdict", Team: "team", "Root base": "root", Platform: "platform", Age: "age", Pushed: "pushed" };
  var EXAMPLES = ['stale in ["stale", "abandoned"]', "stale.base_drift", "tags = 0 and descendants = 0", "not has:sbom", "conflict"];
  function searchView(el, params) {
    var q = params.get("q") || "", page = parseInt(params.get("page") || "1", 10) || 1, limit = 50;
    $("q").value = q;
    var sel = {};
    Object.keys(FACET_PARAMS).forEach(function (n) { sel[FACET_PARAMS[n]] = params.getAll(FACET_PARAMS[n]); });
    if (!el.querySelector("#results")) {
      el.innerHTML = '<div class="head"><div><h1>Search</h1><p class="sub" id="searchsub"></p></div><span id="searchcmd"></span></div>' +
        '<div class="chips" id="examples">' + EXAMPLES.map(function (x) { return '<button type="button" class="chip">' + esc(x) + "</button>"; }).join("") + "</div>" +
        '<p class="qerr" id="qerr" hidden></p>' +
        '<div class="searchgrid"><aside class="facets" id="facets" aria-label="Facets"></aside><div class="panel"><div class="rtable"><table><thead><tr><th>Image</th><th>Team</th><th>Root base</th><th>Verdict</th><th class="r">Age</th><th class="r">Size</th></tr></thead><tbody id="results"></tbody></table></div>' +
        '<div class="pager"><span class="num" id="pageinfo"></span><span><button type="button" id="prev">Previous</button> <button type="button" id="next">Next</button></span></div></div></div>';
    }
    $("results").innerHTML = '<tr><td colspan="6" class="loading">Searching…</td></tr>';
    var p = { filter: q, offset: (page - 1) * limit, limit: limit };
    Object.keys(sel).forEach(function (k) { p[k] = sel[k]; });
    api("v1/search", p).then(function (res) {
      $("qerr").hidden = true;
      $("searchsub").textContent = fmt(res.total) + " images match" + (q || Object.keys(sel).some(function (k) { return sel[k].length; }) ? "" : " (no filter)") + " · facet counts update with every choice" +
        (notBuilt(lastStatus) ? " · verdicts and lineage fields are empty, and searches slower, until the first refresh" : "");
      $("searchcmd").innerHTML = cmdHTML("strata query '" + res.filter.replace(/'/g, "'\\''") + "'");
      $("facets").innerHTML = res.facets.map(function (f) {
        if (!f.values.length) { return ""; }
        var max = Math.max.apply(null, f.values.map(function (v) { return v.count; }).concat([1]));
        return '<div class="facet"><h3>' + esc(f.name) + "</h3>" + f.values.map(function (v) {
          return '<button type="button" class="fv" data-f="' + esc(FACET_PARAMS[f.name]) + '" data-v="' + esc(v.value) + '" aria-pressed="' + v.selected + '" title="' + esc(v.value) + '"><span class="box"></span><span class="l">' +
            (f.name === "Verdict" ? '<i class="dot" style="background:' + (COL[v.value] || "#6b6880") + ';margin-right:6px"></i>' : "") + esc(f.name === "Root base" ? nice(v.value) : v.value) + '</span><span class="c num">' + short(v.count) + '</span><span class="fbar"><i style="width:' + (100 * v.count / max).toFixed(1) + '%"></i></span></button>';
        }).join("") + "</div>";
      }).join("");
      $("results").innerHTML = res.hits.length ? res.hits.map(function (h) {
        return '<tr data-href="#image?d=' + encodeURIComponent(h.digest) + '"><td><a class="linkish" style="color:var(--cream)" href="#image?d=' + encodeURIComponent(h.digest) + '">' + esc(nice(h.name)) + "</a>" + (h.tags.length ? "" : ' <span class="kind">untagged</span>') + "</td><td>" + esc(h.team || "—") + '</td><td class="trunc" title="' + esc(h.root) + '">' + esc(nice(h.root) || "—") + '</td><td><span class="pill"><i class="dot" style="background:' + (COL[h.verdict] || "#6b6880") + '"></i>' + esc(h.verdict || "—") + '</span></td><td class="r num">' + (h.age_days == null ? "—" : h.age_days + "d") + '</td><td class="r num">' + bytes(h.size) + "</td></tr>";
      }).join("") : '<tr><td colspan="6" class="empty-state">No images match. Remove a facet or loosen the filter.</td></tr>';
      var pages = Math.max(1, Math.ceil(res.total / limit));
      $("pageinfo").textContent = res.total ? fmt(res.offset + 1) + "–" + fmt(Math.min(res.total, res.offset + limit)) + " of " + fmt(res.total) : "0 results";
      $("prev").disabled = page <= 1; $("next").disabled = page >= pages;
      $("prev").onclick = function () { setPage(page - 1); };
      $("next").onclick = function () { setPage(page + 1); };
    }).catch(function (e) {
      if (e.status === 400) { $("qerr").textContent = e.message; $("qerr").hidden = false; $("results").innerHTML = '<tr><td colspan="6" class="empty-state">Fix the filter to see results.</td></tr>'; return; }
      $("results").innerHTML = '<tr><td colspan="6" class="empty-state err">Search failed: ' + esc(e.message) + "</td></tr>";
    });
  }
  function searchParams() { var p = parseHash().params; return p; }
  function setPage(n) { var p = searchParams(); p.set("page", String(n)); location.hash = "#search?" + p.toString(); window.scrollTo(0, 0); }
  document.addEventListener("click", function (e) {
    var b = e.target.closest(".fv");
    if (b) {
      var p = searchParams(), f = b.getAttribute("data-f"), v = b.getAttribute("data-v"), vals = p.getAll(f);
      p.delete(f); p.delete("page");
      (vals.indexOf(v) >= 0 ? vals.filter(function (x) { return x !== v; }) : vals.concat([v])).forEach(function (x) { p.append(f, x); });
      location.hash = "#search?" + p.toString();
      return;
    }
    var chip = e.target.closest("#examples .chip");
    if (chip) { go("search", { q: chip.textContent }); return; }
    var tr = e.target.closest("tr[data-href]");
    if (tr && !e.target.closest("a")) { location.hash = tr.getAttribute("data-href"); }
  });
  $("qform").addEventListener("submit", function (e) { e.preventDefault(); go("search", $("q").value.trim() ? { q: $("q").value.trim() } : {}); });
  document.addEventListener("keydown", function (e) { if (e.key === "/" && document.activeElement !== $("q") && !/INPUT|TEXTAREA/.test(document.activeElement.tagName)) { e.preventDefault(); $("q").focus(); } });

  // ---------- image page ----------
  function imageView(el, params) {
    var d = params.get("d");
    if (!d) {
      el.innerHTML = '<div class="head"><div><h1>Image</h1><p class="sub">Open an image from Lineage or Search, or paste a ref.</p></div></div><form class="query" id="refform" style="max-width:640px"><input id="ref" type="text" placeholder="repo:tag, repo@digest or a digest" aria-label="Image ref"></form>';
      $("refform").addEventListener("submit", function (ev) {
        ev.preventDefault();
        api("v1/resolve", { ref: $("ref").value.trim() }).then(function (r) { go("image", { d: r.digest }); }).catch(function (err) { toast(err.message); });
      });
      return;
    }
    el.innerHTML = '<p class="loading">Loading the image…</p>';
    // A link may name the image by ref, such as repo:tag; resolve it and
    // replace the URL with the digest it points at.
    if (!/^sha256:[0-9a-f]{12,64}$/.test(d)) {
      api("v1/resolve", { ref: d }).then(function (r) { location.replace("#image?d=" + encodeURIComponent(r.digest)); })
        .catch(function (e) { el.innerHTML = '<p class="loading err">' + (e.status === 404 ? "This image is not in the index." : "Could not resolve " + esc(d) + ": " + esc(e.message)) + "</p>"; });
      return;
    }
    // A digest's colon is valid in a path segment, and the API's path
    // pattern expects it unescaped.
    api("v1/images/" + d).then(function (ex) { renderImage(el, ex); })
      .catch(function (e) { el.innerHTML = '<p class="loading err">' + (e.status === 404 ? "This image is not in the index." : "Could not load the image: " + esc(e.message)) + "</p>"; });
  }
  function renderImage(el, ex) {
    ex.layers = ex.layers || []; ex.referrers = ex.referrers || []; ex.findings = ex.findings || [];
    var im = ex.image, st = ex.staleness, li = ex.lineage;
    var v = st.verdict || "";
    var chain = (li.ancestry && li.ancestry.nodes ? li.ancestry.nodes.slice() : []).sort(function (a, b) { return a.depth - b.depth; });
    var W = 210, Hc = 58, gap = 42, svgW = Math.max(1, chain.length) * W + Math.max(0, chain.length - 1) * gap + 4;
    var svg = '<svg width="' + svgW + '" height="' + (Hc + 8) + '" viewBox="0 0 ' + svgW + " " + (Hc + 8) + '" role="img" aria-label="Ancestry chain">';
    chain.forEach(function (nd, i) {
      var x = 2 + i * (W + gap), dash = nd.kind === "phantom" ? ' stroke-dasharray="4 3"' : "";
      var t = nice(nd.label); t = t.length > 26 ? t.slice(0, 25) + "…" : t;
      var sub = nd.kind === "external" ? "external base" : nd.kind === "phantom" ? "not in registry" : (nd.id || "").replace(/^phantom:/, "").slice(0, 19);
      svg += '<rect x="' + x + '" y="2" width="' + W + '" height="' + Hc + '" rx="' + (nd.kind === "external" ? 20 : 9) + '" fill="#17152a" stroke="' + (i === 0 ? "#a99bff" : "#3d3860") + '"' + dash + "/>";
      svg += '<rect x="' + x + '" y="2" width="4" height="' + Hc + '" rx="2" fill="' + (COL[nd.verdict] || "#6b6880") + '"/>';
      svg += '<text x="' + (x + 16) + '" y="26" fill="#f4f1ff" font-size="13" font-weight="600" font-family="Inter, system-ui, sans-serif">' + esc(t) + "</text>";
      svg += '<text x="' + (x + 16) + '" y="45" fill="#8e8aa6" font-size="11" font-family="JetBrains Mono, monospace">' + esc(sub) + "</text>";
      if (i < chain.length - 1) { var ax = x + W; svg += '<path d="M' + (ax + 4) + " " + (2 + Hc / 2) + "H" + (ax + gap - 6) + '" stroke="#4a4468" stroke-width="1.5"/><path d="M' + (ax + gap - 10) + " " + (Hc / 2 - 2) + 'l5 4-5 4" stroke="#4a4468" stroke-width="1.5" fill="none"/>'; }
    });
    svg += "</svg>";
    var sources = (li.edges || []).map(function (e) { return e.source + (e.conflict ? " (conflicts with layers)" : ""); });
    var labels = Object.keys(im.labels || {}).sort();
    el.innerHTML =
      '<div class="imghead"><div style="min-width:0"><h1>' + esc(nice(im.label)) + '</h1><div class="digest">' + esc(im.repo) + "@" + esc(im.digest) + "</div></div>" + cmdHTML("strata explain " + (im.tags && im.tags.length ? im.tags[0] : im.repo + "@" + im.digest)) + "</div>" +
      '<div class="panel"><header><h2>Staleness</h2><span class="pill"><i class="dot" style="background:' + (COL[v] || "#6b6880") + '"></i>' + esc(v || "not computed") + '</span></header><div class="body"><ul class="reasons">' +
      ((st.reasons && st.reasons.length) ? st.reasons.map(function (r) { return '<li><i class="dot" style="background:' + (COL[v] || "#8e8aa6") + '"></i><span>' + esc(r) + "</span></li>"; }).join("") : '<li><i class="dot" style="background:' + COL.fresh + '"></i><span>On its current base, tagged, and nothing in its labels says otherwise.</span></li>') + "</ul></div></div>" +
      '<div class="panel"><header><h2>Ancestry</h2>' + (li.parent ? '<button type="button" class="chip" data-focus="' + esc(li.parent) + '">Open parent in Lineage</button>' : "") + '</header><div class="body chain">' + (chain.length > 1 ? svg : '<p class="sub">' + (li.status === "unknown" ? "Lineage unknown: one layer and no declared base, as with squashed or FROM scratch images." : "This image is a root: no indexed image is a prefix of its layers.") + "</p>") +
      (sources.length || li.descendants ? '<p class="sub" style="margin-top:10px">' + [sources.length ? "Parent evidence: " + esc(sources.join(", ")) : "", li.descendants ? fmt(li.descendants) + " image" + (li.descendants === 1 ? " is" : "s are") + " built on this one" : ""].filter(Boolean).join(" · ") + "</p>" : "") + "</div></div>" +
      '<div class="grid2"><div class="panel"><header><h2>Layers</h2><span class="pct">' + im.layer_count + " layers · " + bytes(im.size) + '</span></header><div class="rtable"><table class="layers"><thead><tr><th class="r">#</th><th class="r">Size</th><th>Built by</th><th class="r">Shared by</th></tr></thead><tbody>' +
      ex.layers.map(function (l) { return '<tr><td class="r num">' + l.position + '</td><td class="r num">' + bytes(l.size) + "</td><td>" + esc(l.created_by || "—") + '</td><td class="r num">' + short(l.shared_by) + "</td></tr>"; }).join("") + "</tbody></table></div></div>" +
      '<div class="panel"><header><h2>Image</h2></header><div class="body"><dl class="kv">' +
      "<dt>Tags</dt><dd>" + (im.tags && im.tags.length ? esc(im.tags.map(nice).join(", ")) : "none") + "</dd>" +
      "<dt>Platform</dt><dd>" + esc(im.platform) + (im.platforms && im.platforms.length > 1 ? ' <span class="pct">(index: ' + esc(im.platforms.join(", ")) + ")</span>" : "") + "</dd>" +
      "<dt>Created</dt><dd>" + (im.created ? esc(im.created.slice(0, 10)) + " (" + im.age_days + " days ago)" : "unknown") + "</dd>" +
      (im.pushed ? "<dt>Pushed</dt><dd>" + esc(im.pushed.slice(0, 10)) + "</dd>" : "") +
      (im.first_seen ? "<dt>First seen</dt><dd>" + esc(im.first_seen.slice(0, 10)) + "</dd>" : "") +
      "<dt>Referrers</dt><dd>" + (ex.referrers.length ? esc(ex.referrers.map(function (r) { return r.kind; }).join(", ")) : "none") + "</dd>" +
      "<dt>Runs as</dt><dd>" + esc(im.config.user || "root (empty)") + "</dd>" +
      (im.config.entrypoint && im.config.entrypoint.length ? '<dt>Entrypoint</dt><dd class="mono">' + esc(im.config.entrypoint.join(" ")) + "</dd>" : "") +
      (im.config.ports && im.config.ports.length ? "<dt>Ports</dt><dd>" + esc(im.config.ports.join(", ")) + "</dd>" : "") +
      (im.config.env_keys && im.config.env_keys.length ? '<dt>Env keys</dt><dd class="mono">' + esc(im.config.env_keys.join(", ")) + "</dd>" : "") +
      labels.map(function (k) { return '<dt class="mono">' + esc(k) + "</dt><dd>" + esc(im.labels[k]) + "</dd>"; }).join("") +
      "</dl>" + (ex.findings.length ? '<h2 style="margin-top:18px">Findings</h2><div class="find" style="margin-top:8px">' + ex.findings.map(function (f) { return "<div>" + esc(f) + "</div>"; }).join("") + "</div>" : "") + "</div></div></div>";
  }

  // ---------- cleanup ----------
  function exportLink(path, params, label) {
    var u = new URL(path, location.href);
    Object.keys(params).forEach(function (k) { if (params[k]) { u.searchParams.set(k, params[k]); } });
    u.searchParams.set("format", "csv");
    return '<a class="chip" href="' + esc(u.toString()) + '" download>' + esc(label) + "</a>";
  }
  function cleanupView(el, params) {
    var scope = params.get("scope") === "repo" ? "repo" : "registry", where = params.get("where") || "";
    el.innerHTML = '<div class="head"><div><h1>Cleanup</h1><p class="sub">Images safe to delete, abandoned by default: untagged, nothing built on them, not pulled in the policy window. Reclaimable counts each layer once, and only when no kept image still uses it. strata never deletes anything.</p></div>' +
      cmdHTML("strata cleanup" + (where ? " --where '" + where.replace(/'/g, "'\\''") + "'" : "") + (scope === "repo" ? " --scope repo" : "") + " -o csv") + "</div>" +
      '<form class="query" id="cform" style="max-width:760px"><input id="cwhere" type="text" value="' + esc(where) + '" placeholder=\'Candidates (default: stale = "abandoned")\' aria-label="Candidate filter"></form>' +
      '<div class="chips"><button type="button" class="chip" data-scope="registry" aria-pressed="' + (scope === "registry") + '">Layers shared anywhere are kept</button><button type="button" class="chip" data-scope="repo" aria-pressed="' + (scope === "repo") + '">Layers are stored per repo</button></div>' +
      '<p class="loading">Working out what can go…</p>';
    $("cform").addEventListener("submit", function (ev) { ev.preventDefault(); var p = {}; if ($("cwhere").value.trim()) { p.where = $("cwhere").value.trim(); } if (scope === "repo") { p.scope = "repo"; } go("cleanup", p); });
    var body = el.querySelector(".loading");
    Promise.all([api("v1/cleanup", { where: where, scope: scope, limit: 100 }), api("v1/cleanup/lifecycle").catch(function () { return null; })]).then(function (r) {
      var c = r[0], lcs = r[1] || [], maxR = Math.max.apply(null, c.repos.map(function (x) { return x.reclaimable_bytes; }).concat([1]));
      var withPolicy = lcs.filter(function (l) { return l.policy; }).length, skipped = lcs.filter(function (l) { return !l.policy; });
      body.outerHTML =
        (notBuilt(lastStatus) ? pendingHTML(lastStatus) : "") +
        '<div class="kpis">' + [
          ["Reclaimable", bytes(c.reclaimable_bytes), "of " + bytes(c.size) + " the manifests state"],
          ["Candidates", fmt(c.total), fmt(c.images) + " images"],
          ["Repos", fmt(c.repos.length), "with candidates"],
          ["Held back", fmt(c.held), "kept images are built on them or share their index"],
          ["Export", exportLink("v1/cleanup", { where: where, scope: scope }, "Download CSV"), "every candidate"]
        ].map(function (k) { return '<div class="kpi"><div class="k">' + k[0] + '</div><div class="v num">' + k[1] + '</div><div class="d">' + k[2] + "</div></div>"; }).join("") + "</div>" +
        (c.total ? '<div class="grid2"><div class="panel"><header><h2>By repo</h2><span class="pct">freed by deleting that repo\'s candidates</span></header><div class="body"><div class="bars">' +
          c.repos.slice(0, 15).map(function (x) {
            return '<div class="bar" data-search-where="' + esc(x.repo) + '" role="button" tabindex="0"><span class="n"><em>' + esc(nice(x.repo)) + '</em></span><span class="c num">' + bytes(x.reclaimable_bytes) + " · " + fmt(x.candidates) + '</span><div class="mix"><i style="width:' + (100 * x.reclaimable_bytes / maxR).toFixed(1) + '%;background:var(--brand)"></i></div></div>';
          }).join("") + "</div></div></div>" +
          '<div class="panel"><header><h2>ECR lifecycle policies</h2>' + cmdHTML("strata cleanup lifecycle -o script") + '</header><div class="body"><p class="sub" style="margin:0 0 10px">One rule per repo expiring untagged images past the abandoned window, checked against the index first. ' +
          fmt(withPolicy) + " of " + fmt(lcs.length) + " repos get a policy.</p>" +
          (skipped.length ? '<ul class="changes">' + skipped.slice(0, 6).map(function (l) { return '<li><span class="when">skipped</span><span><b>' + esc(nice(l.repo)) + "</b> " + esc(l.skipped) + (l.conflicts.length ? ": " + esc(l.conflicts[0].reason) : "") + "</span></li>"; }).join("") + "</ul>" : "") + "</div></div></div>" +
          '<div class="panel"><header><h2>Candidates</h2><span class="pct">' + (c.candidates.length < c.total ? "largest " + fmt(c.candidates.length) + " of " + fmt(c.total) + " · download the CSV for all" : "largest first") + '</span></header><div class="rtable"><table><thead><tr><th>Repo</th><th>Digest</th><th class="r">Images</th><th class="r">Size</th><th class="r">Frees alone</th><th class="r">Pushed</th><th class="r">Last pull</th><th>Why</th></tr></thead><tbody>' +
          c.candidates.map(function (x) {
            return '<tr data-href="#image?d=' + encodeURIComponent(x.digest) + '"><td>' + esc(nice(x.repo)) + '</td><td class="mono">' + esc(x.digest.slice(7, 19)) + '</td><td class="r num">' + fmt(x.images) + '</td><td class="r num">' + bytes(x.size) + '</td><td class="r num">' + bytes(x.exclusive_bytes) + '</td><td class="r num">' + (x.pushed_days == null ? "—" : x.pushed_days + "d ago") + '</td><td class="r num">' + (x.last_pulled_days == null ? "never" : x.last_pulled_days + "d ago") + '</td><td class="trunc pct" title="' + esc(x.reason) + '">' + esc(x.reason || "—") + "</td></tr>";
          }).join("") + "</tbody></table></div></div>"
          : '<div class="panel"><div class="body"><p class="empty-state">No candidates' + (where ? " match this filter" : ": nothing is abandoned") + ".</p></div></div>");
    }).catch(function (e) {
      if (e.status === 400) { body.outerHTML = '<p class="qerr">' + esc(e.message) + "</p>"; return; }
      body.outerHTML = '<p class="loading err">Could not load cleanup candidates: ' + esc(e.message) + "</p>";
    });
  }
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-scope]");
    if (b) { var p = parseHash().params; p.set("scope", b.getAttribute("data-scope")); location.hash = "#cleanup?" + p.toString(); return; }
    var r = e.target.closest("[data-search-where]");
    if (r) { go("cleanup", { where: (parseHash().params.get("where") || 'stale = "abandoned"') + ' and repo = "' + r.getAttribute("data-search-where") + '"' }); }
  });

  // ---------- routing ----------
  function route() {
    var r = parseHash();
    VIEWS.forEach(function (x) { $("v-" + x).hidden = x !== r.view; });
    document.querySelectorAll(".rail a[data-view]").forEach(function (a) { if (a.getAttribute("data-view") === r.view) { a.setAttribute("aria-current", "page"); } else { a.removeAttribute("aria-current"); } });
    tip.hidden = true;
    var el = $("v-" + r.view);
    if (r.view === "overview") { overview(el); }
    if (r.view === "lineage") { lineageView(el, r.params); }
    if (r.view === "search") { searchView(el, r.params); }
    if (r.view === "cleanup") { cleanupView(el, r.params); }
    if (r.view === "image") { imageView(el, r.params); }
    if (r.view !== "search") { $("q").value = ""; }
  }
  // ---------- background index ----------
  // strata serve --reindex-every crawls in the background. While it runs,
  // every view answers from what is indexed so far; this banner shows the
  // crawl's progress and offers to reload once the views refresh.
  var shown = null, lastStatus = null;
  function clock(iso) { return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); }
  function pollStatus() {
    api("v1/status").then(function (st) {
      var bar = $("indexing"), c = st.crawl, refreshed = st.refreshed_at || "";
      var before = lastStatus;
      lastStatus = st;
      if (shown === null) { shown = refreshed; }
      var newer = refreshed !== shown;
      bar.classList.toggle("err", !st.running && !!st.error);
      if (st.running) {
        var done = c.repos + c.repos_resumed, pct = c.repos_total ? 100 * done / c.repos_total : 0;
        var secs = st.refresh_every_seconds, every = secs >= 60 ? Math.round(secs / 60) + " min" : secs + " s";
        bar.innerHTML = "<span>Indexing <b>" + esc(nice(st.registry || "")) + "</b></span>" +
          '<span class="ibar" aria-hidden="true"><i style="width:' + pct.toFixed(1) + '%"></i></span>' +
          '<span class="num">' + (c.repos_total ? fmt(done) + " of " + fmt(c.repos_total) + " repos · " : "listing repos · ") + fmt(c.new_images) + " new images</span>" +
          '<span class="when">' + (refreshed ? "Results as of " + clock(refreshed) : "No results yet") + (secs ? ", refreshing every " + every : ", refreshing when the crawl finishes") + "</span>" +
          (newer ? '<button type="button" id="ilatest">Show latest</button>' : "");
      } else if (st.error) {
        bar.innerHTML = '<span class="err">Indexing ' + esc(nice(st.registry || "")) + " failed: " + esc(st.error) + "</span>" + (newer ? '<button type="button" id="ilatest">Show latest</button>' : "");
      } else if (st.refreshing) {
        bar.innerHTML = "<span>Computing lineage and staleness" + (refreshed ? "" : " for the first time") + '…</span><span class="when">On a large index this takes several minutes.</span>';
      } else if (newer) {
        bar.innerHTML = (c.repos_total ? "<span>Indexing finished: <b>" + fmt(c.new_images) + "</b> new images</span>" : "<span>Lineage and staleness are up to date.</span>") + '<button type="button" id="ilatest">Show latest</button>';
      }
      bar.hidden = !st.running && !st.error && !st.refreshing && !newer;
      // The first status can arrive after a view has rendered its empty
      // state; render it again with the reason.
      if (!before && notBuilt(st)) { route(); }
      var b = $("ilatest");
      if (b) { b.onclick = function () { location.reload(); }; }
      setTimeout(pollStatus, st.running ? 5000 : 60000);
    }).catch(function () { setTimeout(pollStatus, 60000); });
  }
  pollStatus();
  addEventListener("hashchange", route);
  var rz; addEventListener("resize", function () { clearTimeout(rz); rz = setTimeout(function () { if (!$("v-overview").hidden) { drawIce(); } if (!$("v-lineage").hidden) { paint(); } }, 120); });
  api("v1/overview").then(function (o) { host = o.host || ""; $("foot").innerHTML = "Index<b>" + esc(o.host || "all registries") + "</b>" + (o.indexed_at ? "crawled " + ago(o.indexed_at) : ""); route(); }).catch(function () { route(); });
})();
