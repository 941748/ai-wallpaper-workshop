// 向导: 4 步流程(连接 → 问卷 → 首张壁纸 → 完成)
// 问卷筹码组件与设置页"偏好"共用(挂 window.SurveyUI)

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
        st.answers[key] = (st.answers[key] + 1) % 3; // 0→1→2→0
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

// ---------- 向导主流程 ----------
window.initWizard = function () {
  let step = 0;
  let renderOK = false;
  let firstOK = false;
  let firstBusy = false;
  let accepted = false;
  let survey = null;
  let surveyBase = { answers: {}, selectedPacks: [] };
  let countdownTimer = null;

  const $ = (id) => document.getElementById(id);

  function goto(n) {
    step = n;
    document.querySelectorAll(".wiz-step").forEach((el) => {
      const s = +el.dataset.step;
      el.classList.toggle("active", s === n);
      el.classList.toggle("done", s < n);
    });
    document.querySelectorAll(".wiz-page").forEach((el, i) =>
      el.classList.toggle("active", i === n));
    if (n === 1 && !survey) {
      survey = mountSurvey({
        packsRoot: $("wiz-packs"), tabsRoot: $("wiz-dimtabs"),
        chipsRoot: $("wiz-chips"), likesEl: $("wiz-likes"),
        dislikesEl: $("wiz-dislikes"), summaryEl: $("wiz-summary"),
      });
      B.GetSurvey().then((data) => {
        survey.load(data);
        surveyBase = survey.dump();
      });
    }
    if (n === 2) startFirst();
  }

  $("wiz-test").onclick = async () => {
    const btn = $("wiz-test");
    btn.disabled = true;
    setStatus($("wiz-conn-status"), "测试中...");
    try {
      const r = await B.TestConnection({});
      setStatus($("wiz-conn-status"), r.message, r.ok ? (r.render ? "ok" : "hint") : "err");
      renderOK = r.render;
    } catch (e) {
      setStatus($("wiz-conn-status"), "测试失败: " + e, "err");
    }
    btn.disabled = false;
  };

  $("wiz-next-0").onclick = async () => {
    const btn = $("wiz-next-0");
    btn.disabled = true;
    setStatus($("wiz-conn-status"), "连接中...");
    try {
      const r = await B.SaveConn({});
      if (!r.ok) {
        setStatus($("wiz-conn-status"), r.message, "err");
        btn.disabled = false;
        return;
      }
      renderOK = r.render;
      goto(1);
    } catch (e) {
      setStatus($("wiz-conn-status"), "连接失败: " + e, "err");
    }
    btn.disabled = false;
  };

  // ----- 步骤1: 问卷 -----
  $("wiz-next-1").onclick = async () => {
    const { likes } = survey.counts();
    const dump = survey.dump();
    if (dump.selectedPacks.length === 0 && likes === 0) {
      await modal({
        title: "需要至少一项偏好",
        body: "请至少选择 1 个风格包或勾选 1 项\"喜欢\", 我才能知道你想看什么。",
      });
      return;
    }
    // 与上次一致则不重复落盘
    const changed = survey.isDirty(surveyBase);
    if (changed) {
      try {
        await B.SaveSurvey({
          answers: dump.answers, selectedPacks: dump.selectedPacks,
          reason: "初始化问卷完成: 风格包 " + dump.selectedPacks.length +
            " 个 · 喜欢 " + likes + " 项",
        });
        surveyBase = dump;
      } catch (e) {
        await modal({ title: "保存失败", body: "" + e });
        return;
      }
    }
    goto(2);
  };
  $("wiz-back-1").onclick = () => goto(0);
  $("wiz-back-2").onclick = () => goto(1);
  $("wiz-cleardim").onclick = () => survey && survey.clearDim();

  // ----- 步骤2: 首张壁纸 -----
  function startCountdown() {
    const start = Date.now();
    const limit = 90000;
    $("wiz-progress").classList.remove("hidden");
    clearInterval(countdownTimer);
    countdownTimer = setInterval(() => {
      let el = Date.now() - start;
      if (el > limit) el = limit;
      const pct = Math.min(95, Math.round((el * 100) / limit));
      $("wiz-progress-bar").style.width = pct + "%";
      const remain = Math.max(0, Math.ceil((limit - el) / 1000));
      setStatus($("wiz-first-status"),
        "正在生成第一张壁纸… 通常 30–60 秒, 最多再等 " + remain + " 秒后自动完成", "hint");
    }, 500);
  }
  function stopCountdown() {
    clearInterval(countdownTimer);
    $("wiz-progress").classList.add("hidden");
    $("wiz-progress-bar").style.width = "0%";
  }

  async function genFirst(force) {
    if ($("wiz-next-2").disabled) return;
    $("wiz-next-2").disabled = true;
    $("wiz-again").disabled = true;
    $("wiz-preview").classList.add("hidden");
    $("wiz-preview-empty").classList.remove("hidden");
    startCountdown();
    try {
      const r = await B.GenFirstWallpaper(force);
      stopCountdown();
      if (r.busy || !r.ok) {
        firstBusy = true; firstOK = false;
        setStatus($("wiz-first-status"), r.message || "系统繁忙, 初始化完成后会自动无感补图", "hint");
        $("wiz-preview-empty").textContent = "稍后自动补图";
        $("wiz-again").textContent = "重试";
        $("wiz-next-2").textContent = "跳过(稍后自动补图)";
        $("wiz-next-2").disabled = false;
        $("wiz-again").disabled = false;
        return;
      }
      firstOK = true; firstBusy = false;
      const img = $("wiz-preview");
      img.src = "data:image/jpeg;base64," + r.image;
      img.classList.remove("hidden");
      $("wiz-preview-empty").classList.add("hidden");
      setStatus($("wiz-first-status"), "已应用为桌面壁纸 ✓ 喜欢就点\"喜欢\"; 不满意点\"换一张\"", "ok");
      $("wiz-again").textContent = "换一张";
      $("wiz-next-2").textContent = "喜欢, 就用这张";
      $("wiz-next-2").disabled = false;
      $("wiz-again").disabled = false;
    } catch (e) {
      stopCountdown();
      setStatus($("wiz-first-status"), "生成失败: " + e, "err");
      $("wiz-next-2").disabled = false;
      $("wiz-again").disabled = false;
    }
  }

  function startFirst() {
    $("wiz-again").textContent = "换一张";
    $("wiz-next-2").textContent = "喜欢, 就用这张";
    if (firstOK) return; // 返回重进: 保留已生成的
    if (!renderOK) {
      firstBusy = true;
      setStatus($("wiz-first-status"), "系统繁忙, 初始化完成后会自动无感补图", "hint");
      $("wiz-preview-empty").textContent = "稍后自动补图";
      $("wiz-again").textContent = "重试";
      $("wiz-next-2").textContent = "跳过(稍后自动补图)";
      return;
    }
    genFirst(false);
  }

  $("wiz-again").onclick = () => {
    if (firstBusy && !firstOK) {
      // 重试: 重新探测
      B.TestConnection({}).then((r) => {
        renderOK = r.render;
        if (r.render) genFirst(false);
        else setStatus($("wiz-first-status"), "系统繁忙, 初始化完成后会自动无感补图", "hint");
      });
      return;
    }
    genFirst(true); // 换一张(排除最近主风格)
  };

  $("wiz-next-2").onclick = async () => {
    if (firstOK && !accepted) {
      accepted = true; // 记喜欢信号在 FinishWizard(accepted)
    }
    goto(3);
  };

  // ----- 步骤3: 完成 -----
  // 进入完成页时执行静默安装
  let finished = false;
  const origGoto = goto;
  goto = function (n) {
    origGoto(n);
    if (n === 3 && !finished) {
      finished = true;
      $("wiz-next-2").disabled = true;
      B.FinishWizard(firstOK && accepted).then((r) => {
        setStatus($("wiz-reg-status"), r.message, r.regOk ? "ok" : "hint");
        $("wiz-recap").textContent = r.recap || "";
      }).catch((e) => {
        setStatus($("wiz-reg-status"), "注册异常: " + e, "hint");
      });
    }
  };

  $("wiz-finish").onclick = () => B.QuitApp();

  goto(0);
};
