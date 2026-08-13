// 列数を最初のペイントより前に <html data-cols> へ反映する（D-43 / P-FOUC-COLS）。
(function () {
  try {
    var c = window.localStorage.getItem("seekx.cols");
    if (c === "2" || c === "3") document.documentElement.setAttribute("data-cols", c);
  } catch (e) { /* 使えない構成では既定の 1 列（P-LS-THROW） */ }
})();
