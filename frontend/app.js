// AI 壁纸工坊 Wails 前端(原生 JS, 无构建步骤)
// Go↔JS 绑定: window.go.ui.Bridge.<Method>(Wails 运行时注入, 均返回 Promise)

const B = window.go.ui.Bridge;

// ---------- 通用: 状态行 ----------
function setStatus(el, text, cls) {
  if (!el) return;
  el.textContent = text || "";
  el.className = "status" + (cls ? " " + cls : "");
}

// ---------- 通用: 弹层 ----------
// modal({title, body, input, buttons:[{text, primary, danger, onClick}]})
// input=true → 显示文本输入, buttons onClick 收到 (inputValue, close)
function modal(opts) {
  return new Promise((resolve) => {
    const m = document.getElementById("modal");
    document.getElementById("modal-title").textContent = opts.title || "";
    const body = document.getElementById("modal-body");
    if (opts.html) {
      body.innerHTML = opts.html;
    } else {
      body.textContent = opts.body || "";
    }
    const iw = document.getElementById("modal-input-wrap");
    const ii = document.getElementById("modal-input");
    iw.classList.toggle("hidden", !opts.input);
    ii.value = opts.inputValue || "";
    const btns = document.getElementById("modal-btns");
    btns.innerHTML = "";
    const close = (val) => { m.classList.add("hidden"); resolve(val); };
    (opts.buttons || [{ text: "确定" }]).forEach((b) => {
      const el = document.createElement("button");
      el.className = "btn" + (b.primary ? " primary" : "") + (b.danger ? " danger" : "");
      el.textContent = b.text;
      el.onclick = () => {
        if (b.onClick) {
          const r = b.onClick(opts.input ? ii.value : undefined, close);
          if (r === false) return; // onClick 返回 false = 不关闭
        }
        close(b.value !== undefined ? b.value : (opts.input ? ii.value : true));
      };
      btns.appendChild(el);
    });
    m.classList.remove("hidden");
    if (opts.input) setTimeout(() => ii.focus(), 50);
  });
}

// 确认框(是/否)
function confirmBox(title, body) {
  return modal({
    title, body,
    buttons: [
      { text: "否", value: false },
      { text: "是", primary: true, value: true },
    ],
  });
}

// ---------- 通用: 侧栏路由 ----------
function initNav() {
  document.getElementById("nav").addEventListener("click", (e) => {
    const btn = e.target.closest(".nav-item");
    if (!btn) return;
    switchPage(btn.dataset.page);
  });
}

function switchPage(page) {
  document.querySelectorAll(".nav-item").forEach((b) =>
    b.classList.toggle("active", b.dataset.page === page));
  document.querySelectorAll(".page").forEach((p) =>
    p.classList.toggle("active", p.id === "page-" + page));
  if (page === "current" && window.loadCurrent) window.loadCurrent();
  if (page === "prefs" && window.loadPrefs) window.loadPrefs();
  if (page === "history" && window.loadHistory) window.loadHistory();
  if (page === "settings" && window.loadSettings) window.loadSettings();
}

// ---------- 启动 ----------
async function boot() {
  try {
    const info = await B.Boot();
    document.getElementById("version-label").textContent = "v" + info.version;
    window.__boot = info;
    if (info.initialized) {
      document.getElementById("app").classList.remove("hidden");
      initNav();
      if (window.initCurrent) window.initCurrent();
      if (window.initPrefs) window.initPrefs();
      if (window.initHistory) window.initHistory();
      if (window.initSettings) window.initSettings();
    } else {
      document.getElementById("wizard").classList.remove("hidden");
      if (window.initWizard) window.initWizard();
    }
  } catch (err) {
    document.body.innerHTML =
      '<div style="padding:40px;color:#f85149">启动失败: ' + err + "</div>";
  }
}
boot();
