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

// ---------- 共享: 问卷筹码组件 ----------
// mountSurvey({root, packsRoot, tabsRoot, chipsRoot, likesEl, dislikesEl, summaryEl, onChanged})
function mountSurvey(o) {
  const st = { dims: [], packs: [], answers: {}, selected: new Set(), dimIdx: 0 };

  function covered(key) {
    const [dim, val] = key.split("/");
    for (const name of st.selected) {
      const p = st.packs.find((x) => x.name === name);
      if (p && p.vals[dim] && p.vals[dim].includes(val)) return true;
    }
    return false;
  }

  function renderPacks() {
    o.packsRoot.innerHTML = "";
    st.packs.forEach((p) => {
      const el = document.createElement("button");
      el.className = "pack-chip" + (st.selected.has(p.name) ? " on" : "");
      el.textContent = p.name;
      el.onclick = () => {
        if (st.selected.has(p.name)) st.selected.delete(p.name);
        else st.selected.add(p.name);
        renderPacks(); renderChips(); stats();
        if (o.onChanged) o.onChanged();
      };
      o.packsRoot.appendChild(el);
    });
  }

  function renderTabs() {
    o.tabsRoot.innerHTML = "";
    st.dims.forEach((d, i) => {
      const el = document.createElement("button");
      el.className = "dim-tab" + (i === st.dimIdx ? " active" : "");
      el.textContent = d.nameCn;
      el.onclick = () => { st.dimIdx = i; renderTabs(); renderChips(); };
      o.tabsRoot.appendChild(el);
    });
  }

  function renderChips() {
    o.chipsRoot.innerHTML = "";
    const dim = st.dims[st.dimIdx];
    if (!dim) return;
    dim.values.forEach((v) => {
      const key = dim.id + "/" + v.id;
      const a = st.answers[key] || 0;
      const el = document.createElement("button");
      el.className = "chip" + (a === 1 ? " like" : a === 2 ? " dislike" : "") +
        (covered(key) ? " covered" : "");
      el.textContent = v.nameCn + (a === 1 ? " ✓" : a === 2 ? " ✕" : "");
      el.onclick = () => {
        // ||0: 重置清表单后 answers 为空对象, undefined+1 会算出 NaN 导致点击永不生效
        st.answers[key] = ((st.answers[key] || 0) + 1) % 3; // 0→1→2→0
        renderChips(); stats();
        if (o.onChanged) o.onChanged();
      };
      o.chipsRoot.appendChild(el);
    });
  }

  function stats() {
    let likes = 0, dislikes = 0;
    for (const k in st.answers) {
      if (st.answers[k] === 1) likes++;
      else if (st.answers[k] === 2) dislikes++;
    }
    if (o.likesEl) o.likesEl.textContent = likes;
    if (o.dislikesEl) o.dislikesEl.textContent = dislikes;
    if (o.summaryEl) {
      o.summaryEl.textContent = st.selected.size
        ? "风格包: " + [...st.selected].join(" + ")
        : "风格包: 未选(按喜好自由组合)";
    }
  }

  function clearDim() {
    const dim = st.dims[st.dimIdx];
    if (!dim) return;
    dim.values.forEach((v) => { st.answers[dim.id + "/" + v.id] = 0; });
    renderChips(); stats();
    if (o.onChanged) o.onChanged();
  }

  return {
    load(data) { // SurveyData → 初始化
      st.dims = data.dims || [];
      st.packs = data.packs || [];
      st.answers = Object.assign({}, data.answers);
      st.selected = new Set(data.selectedPacks || []);
      st.dimIdx = 0;
      renderPacks(); renderTabs(); renderChips(); stats();
    },
    clearDim,
    // 保存用: 序列化(过滤全 0 中立项, 减小 payload)
    dump() {
      const ans = {};
      for (const k in st.answers) if (st.answers[k] !== 0) ans[k] = st.answers[k];
      return { answers: ans, selectedPacks: [...st.selected] };
    },
    counts() {
      let likes = 0, dislikes = 0;
      for (const k in st.answers) {
        if (st.answers[k] === 1) likes++;
        else if (st.answers[k] === 2) dislikes++;
      }
      return { likes, dislikes };
    },
    isDirty(base) {
      const now = this.dump();
      const a = JSON.stringify(Object.keys(now.answers).sort().map((k) => [k, now.answers[k]]));
      const b = JSON.stringify(Object.keys(base.answers).sort().map((k) => [k, base.answers[k]]));
      const pa = JSON.stringify([...now.selectedPacks].sort());
      const pb = JSON.stringify([...base.selectedPacks].sort());
      return a !== b || pa !== pb;
    },
  };
}
window.mountSurvey = mountSurvey;

// ---------- 启动 ----------
async function boot() {
  try {
    const info = await B.Boot();
    document.getElementById("version-label").textContent = "v" + info.version;
    window.__boot = info;
    // 初始化向导已移除(负责人 2026-11-09: 无用户决策点): 首装注册/默认包/计划任务
    // 由 Go 侧 FirstRunSetup 后台静默完成, 直接进设置页
    document.getElementById("app").classList.remove("hidden");
    initNav();
    if (window.initCurrent) window.initCurrent();
    if (window.initPrefs) window.initPrefs();
    if (window.initHistory) window.initHistory();
    if (window.initSettings) window.initSettings();
  } catch (err) {
    document.body.innerHTML =
      '<div style="padding:40px;color:#f85149">启动失败: ' + err + "</div>";
  }
}
boot();
