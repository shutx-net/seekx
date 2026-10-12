// 表示中のフォロワーの CSV（followers.html のデータブロック）をファイルとして保存する。API もサーバも呼ばない。
(function () {
  var btn = document.getElementById("csv-download"), src = document.getElementById("csv-data");
  // 要素が無いページ（フォロワー一覧以外・0 人）と、保存の手段（Blob・Blob URL）が無いブラウザでは何もしない。
  if (!btn || !src || !window.Blob || !window.URL || !URL.createObjectURL) return;
  // 【加工しない】BOM・クォート・無害化はサーバ（view.FollowersCSVFile）で済んでいる。
  var data = JSON.parse(src.textContent); // 先に読む。壊れていれば例外で止まり、ボタンは出ない
  btn.addEventListener("click", function () {
    var url = URL.createObjectURL(new Blob([data.csv], {type: "text/csv;charset=utf-8"}));
    var a = document.createElement("a");
    a.href = url;
    a.download = data.filename;
    document.body.appendChild(a); // 文書の外の a では click() で保存しないブラウザがある
    a.click();
    document.body.removeChild(a);
    // 保存は click() の後で非同期に始まる。すぐ破棄すると読む前に URL が無効になるブラウザがあるので待つ。
    setTimeout(function () { URL.revokeObjectURL(url); }, 40000);
  });
  btn.removeAttribute("hidden"); // JS が動かない環境では見せない（D-52）
})();
