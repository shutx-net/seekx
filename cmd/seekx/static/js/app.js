// <dialog> の開閉のみ。フレームワーク・外部 JS は使わない（D-04）。
document.addEventListener("click", function (ev) {
  var t = ev.target.closest ? ev.target.closest("[data-dialog]") : null;
  if (!t) return;
  var d = document.getElementById(t.getAttribute("data-dialog"));
  if (d && typeof d.showModal === "function") {
    ev.preventDefault();
    d.showModal();
  }
});
// 一覧の列数（保存・適用）と、取得済みカードの絞り込み。ネットワークアクセスは無い。
(function () {
  var list = document.getElementById("post-list"), controls = document.getElementById("list-controls");
  if (!list || !controls) { // 一覧が無いページで --max の拡張だけが効くのを防ぐ（P-COLS-LEAK）
    document.documentElement.removeAttribute("data-cols");
    return;
  }
  var header = document.querySelector(".site-header");
  function syncHeaderH() { // 固定した操作部の top に渡すヘッダの実高さ。CSS 側の値は初期値のみ
    document.documentElement.style.setProperty("--header-h", header.offsetHeight + "px");
  }
  if (header && window.ResizeObserver) new ResizeObserver(syncHeaderH).observe(header); // observe() は初回も発火する
  else if (header) syncHeaderH();
  var cards = list.querySelectorAll(".post"); // DOM は変化しないので 1 度だけ取得（A-15）
  var countEl = document.getElementById("filter-count"), emptyEl = document.getElementById("filter-empty"), badgeEl = document.getElementById("filter-summary-count");
  var flagBoxes = [["f-no-reply", "reply"], ["f-no-repost", "repost"], ["f-no-quote", "quote"],
    ["f-no-sensitive", "sensitive"], ["f-no-link", "link"]];
  var timer = null;
  controls.removeAttribute("hidden"); // JS が動かない環境では見せない（D-52）
  // ---- 列数（D-40 / D-42）。属性 1 つの付け外しだけで DOM は作り直さない（AC-09）----
  function applyCols(v) {
    if (v === "2" || v === "3") document.documentElement.setAttribute("data-cols", v);
    else document.documentElement.removeAttribute("data-cols");
    try { window.localStorage.setItem("seekx.cols", v); } catch (e) { /* P-LS-THROW */ }
  }
  var radios = controls.querySelectorAll("input[name='cols']");
  var current = document.documentElement.getAttribute("data-cols") || "1";
  for (var i = 0; i < radios.length; i++) radios[i].checked = (radios[i].value === current);
  // ---- 絞り込み。取得済みの DOM だけを見る（API は呼ばない）----
  function numOf(id) { // 空欄・解釈不能は null にして比較自体を行わない（P-NUMBER-EMPTY）
    var el = document.getElementById(id);
    var n = el && el.value !== "" ? parseInt(el.value, 10) : NaN;
    return isNaN(n) ? null : n;
  }
  function on(id) { var el = document.getElementById(id); return !!(el && el.checked); }
  function apply() {
    var minR = numOf("f-runes-min"), maxR = numOf("f-runes-max"), minM = numOf("f-media-min"), minL = numOf("f-likes-min");
    var excl = [], shown = 0;
    for (var j = 0; j < flagBoxes.length; j++) if (on(flagBoxes[j][0])) excl.push(" " + flagBoxes[j][1] + " ");
    for (var i = 0; i < cards.length; i++) {
      var c = cards[i], ok = true;
      var runes = +c.getAttribute("data-f-runes"), media = +c.getAttribute("data-f-media"), likes = +c.getAttribute("data-f-likes");
      var flags = " " + (c.getAttribute("data-f-flags") || "") + " ";
      if (minR !== null && runes < minR) ok = false;
      if (maxR !== null && runes > maxR) ok = false;
      if (minM !== null && media < minM) ok = false;
      if (minL !== null && (likes < 0 || likes < minL)) ok = false; // -1 は不明（D-49）
      for (var k = 0; k < excl.length; k++) if (flags.indexOf(excl[k]) >= 0) ok = false;
      if (ok) { c.removeAttribute("hidden"); shown++; continue; }
      var d = c.querySelector("dialog[open]"); // 開いたまま隠すと操作不能になり得る（P-DIALOG-HIDDEN）
      if (d) d.close();
      c.setAttribute("hidden", "");
    }
    countEl.textContent = shown + " / " + cards.length + " 件表示中";
    badgeEl.textContent = shown === cards.length ? "" : shown + " / " + cards.length + " 件表示中"; // 空なら CSS の :empty で消える
    if (shown === 0) emptyEl.removeAttribute("hidden");
    else emptyEl.setAttribute("hidden", "");
  }
  function schedule() { clearTimeout(timer); timer = setTimeout(apply, 150); } // 読み上げ対策（D-53 / AC-05）
  function onEdit(ev) {
    if (ev.target.name !== "cols") schedule();
    else if (ev.type === "input") applyCols(ev.target.value); // change では二重に適用しない
  }
  controls.addEventListener("input", onEdit);
  controls.addEventListener("change", onEdit);
  document.getElementById("filter-reset").addEventListener("click", function () {
    var nums = controls.querySelectorAll("input[type='number']");
    var boxes = controls.querySelectorAll("input[type='checkbox']");
    for (var i = 0; i < nums.length; i++) nums[i].value = "";
    for (var j = 0; j < boxes.length; j++) boxes[j].checked = false;
    apply();
  });
  // 初期化時に apply() は呼ばない。サーバが描画した全件表示・「M / M 件表示中」が既に正しい。
})();
