// 设置视图: 当前壁纸页 + 偏好页

// ================= 当前壁纸 =================
// 出图进度文案池(与安卓端 GalleryScreen 逐字一致): 进入阶段洗牌成牌堆,
// 每 1.5s 轮播一条, 走完一轮重洗; {t} 占位替换为生成秒数。
const SWAP_POOLS = {
  plan: [
    "正在分析你的偏好…",
    "AI 正在构思画什么…",
    "从你的风格包里挑选手牌…",
    "正在组合今天的画面…",
    "翻翻你最近喜欢的风格…",
    "给画面挑一束好光线…",
    "让你的画像提供点灵感…",
    "构思一个不重样的构图…",
    "正在酝酿一个新点子…",
    "帮你避开昨天的审美…",
    "挑个你没见过的角度…",
    "今天也要独一无二…",
  ],
  render: [
    "云端生成中… {t}s",
    "GPU 正在渲染细节…",
    "光影与上色处理中…",
    "一笔一笔画出来…",
    "空气感正在到位…",
    "质感逐渐清晰…",
    "高清细节放大中…",
    "快了，别眨眼…",
    "收尾的高光时刻…",
    "颜料还没干，稍等…",
    "曲线和色彩正在碰撞…",
    "{t}s · 马上见面…",
  ],
  down: [
    "正在下载成品…",
    "把新画装进手机…",
    "信号在路上狂奔…",
    "传输中，马上见面…",
    "新壁纸正在过安检…",
  ],
  apply: [
    "正在设置壁纸…",
    "桌面与锁屏一起换新…",
    "正在给桌面铺上新画…",
    "最后一步，揭幕…",
    "换上新装…",
  ],
};

window.initCurrent = function () {
  const $ = (id) => document.getElementById(id);

  $("cur-like").onclick = () => rate("liked", "喜欢");
  $("cur-dislike").onclick = () => rate("disliked", "不喜欢");
  $("cur-swap").onclick = swapNow;

  // ---- 进度轮播(阶段事件驱动, 对齐安卓) ----
  let prog = null; // {stage, deck, deckStage, tick, renderStart, timer, off}
  const shuffle = (a) => {
    const r = a.slice();
    for (let i = r.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [r[i], r[j]] = [r[j], r[i]];
    }
    return r;
  };
  function paintSwap() {
    if (!prog || prog.deck.length === 0) return;
    let raw = prog.deck[prog.tick % prog.deck.length];
    if (raw.indexOf("{t}") >= 0) {
      const sec = prog.renderStart ? Math.floor((Date.now() - prog.renderStart) / 1000) : "…";
      raw = raw.replace("{t}", sec);
    }
    setStatus($("cur-status"), raw, "hint");
  }
  function setStage(s) {
    if (!prog || !SWAP_POOLS[s] || s === prog.deckStage) return;
    prog.deckStage = s;
    prog.deck = shuffle(SWAP_POOLS[s]);
    prog.tick = 0;
    if (s === "render" && !prog.renderStart) prog.renderStart = Date.now();
    paintSwap();
  }
  function startSwapProgress() {
    stopSwapProgress();
    prog = { deckStage: "", deck: [], tick: 0, renderStart: 0, timer: null, off: null };
    setStage("plan");
    if (window.runtime && window.runtime.EventsOn) {
      prog.off = window.runtime.EventsOn("swap-progress", (p) => p && setStage(p.stage));
    }
    prog.timer = setInterval(() => {
      if (!prog) return;
      prog.tick++;
      if (prog.deck.length > 0 && prog.tick % prog.deck.length === 0) {
        prog.deck = shuffle(prog.deck); // 一轮走完重新洗牌, 下轮顺序不同
      }
      paintSwap();
    }, 1500);
  }
  function stopSwapProgress() {
    if (!prog) return;
    clearInterval(prog.timer);
    if (prog.off) prog.off();
    prog = null;
  }

  async function rate(typ, word) {
    try {
      const msg = await B.RateCurrent(typ);
      setStatus($("cur-status"), msg, "ok");
    } catch (e) {
      setStatus($("cur-status"), "" + e, "err");
    }
  }

  async function swapNow() {
    const btn = $("cur-swap");
    if (btn.disabled) return;
    btn.disabled = true;
    startSwapProgress();
    try {
      const r = await B.SwapNow();
      if (r.changed) {
        setStatus($("cur-status"), r.message, "ok");
        if (r.image) {
          $("cur-img").src = "data:image/jpeg;base64," + r.image;
          $("cur-img").classList.remove("hidden");
          $("cur-empty").classList.add("hidden");
        }
      } else {
        setStatus($("cur-status"), r.message, "err");
      }
    } catch (e) {
      setStatus($("cur-status"), "触发失败: " + e, "err");
    } finally {
      stopSwapProgress();
      btn.disabled = false;
    }
  }

  window.loadCurrent = async function () {
    try {
      const cur = await B.GetCurrent();
      if (cur.exists) {
        $("cur-img").src = "data:image/jpeg;base64," + cur.image;
        $("cur-img").classList.remove("hidden");
        $("cur-empty").classList.add("hidden");
      } else {
        $("cur-img").classList.add("hidden");
        $("cur-empty").classList.remove("hidden");
      }
      $("cur-hint").textContent = cur.hint || "";
      setStatus($("cur-status"), "");
    } catch (e) {
      setStatus($("cur-status"), "" + e, "err");
    }
  };
  window.loadCurrent();
};

// ================= 偏好页 =================
window.initPrefs = function () {
  const $ = (id) => document.getElementById(id);
  let survey = null;
  let base = { answers: {}, selectedPacks: [] };
  let keywords = [];
  let dislikesReset = false;

  survey = mountSurvey({
    packsRoot: $("pf-packs"), tabsRoot: $("pf-dimtabs"),
    chipsRoot: $("pf-chips"), likesEl: $("pf-likes"),
    dislikesEl: $("pf-dislikes"), summaryEl: $("pf-summary"),
    onChanged: () => {},
  });

  function renderKeywords() {
    const sel = $("pf-kw-list");
    const keep = sel.selectedIndex;
    sel.innerHTML = "";
    keywords.forEach((k, i) => {
      const opt = document.createElement("option");
      opt.value = i;
      opt.textContent = k;
      sel.appendChild(opt);
    });
    if (keep >= 0 && keep < keywords.length) sel.selectedIndex = keep;
  }

  $("pf-kw-add").onclick = async () => {
    const w = $("pf-kw-input").value.trim();
    if (!w) return;
    try {
      keywords = await B.SaveKeyword("add", w, -1);
      $("pf-kw-input").value = "";
      renderKeywords();
      setStatus($("pf-status"), "关键词已添加 ✓", "ok");
    } catch (e) {
      setStatus($("pf-status"), "" + e, "err");
    }
  };
  $("pf-kw-del").onclick = async () => {
    const idx = $("pf-kw-list").selectedIndex;
    if (idx < 0) {
      setStatus($("pf-status"), "请先在列表中选中要删除的关键词。", "hint");
      return;
    }
    try {
      keywords = await B.SaveKeyword("del", keywords[idx], idx);
      renderKeywords();
      setStatus($("pf-status"), "关键词已删除 ✓", "ok");
    } catch (e) {
      setStatus($("pf-status"), "" + e, "err");
    }
  };
  $("pf-ctx").onchange = async (e) => {
    try {
      await B.SetContextMode(e.target.checked);
      setStatus($("pf-status"), e.target.checked ? "已开启节日氛围" : "已关闭节日氛围", "ok");
    } catch (err) {
      setStatus($("pf-status"), "" + err, "err");
    }
  };

  $("pf-save").onclick = async () => {
    const { likes } = survey.counts();
    const dump = survey.dump();
    if (dump.selectedPacks.length === 0 && likes === 0) {
      await modal({ title: "无法保存",
        body: "请至少选择 1 个风格包或勾选 1 项\"喜欢\", 再保存偏好。" });
      return;
    }
    try {
      await B.SaveSurvey({
        answers: dump.answers, selectedPacks: dump.selectedPacks,
        reason: "设置页调整偏好",
      });
      base = dump;
      dislikesReset = false;
      setStatus($("pf-status"), "偏好已保存。出图将按你选的风格包轮换, 并严格遵从你勾选的画风与题材。", "ok");
    } catch (e) {
      setStatus($("pf-status"), "" + e, "err");
    }
  };

  $("pf-reset").onclick = async () => {
    const ok = await confirmBox("重置所有偏好",
      "将清空风格包与五维偏好(喜欢/不喜欢全部回到未选), 自定义关键词保留。\n" +
      "在本页重新选择后点\"保存偏好\"即可生效, 无需重跑向导。\n\n确定重置吗?");
    if (!ok) return;
    try {
      await B.ResetPrefs();
      dislikesReset = true;
      await window.loadPrefs();
      setStatus($("pf-status"), "已重置为未选 — 重新选择后点\"保存偏好\"生效", "hint");
    } catch (e) {
      setStatus($("pf-status"), "" + e, "err");
    }
  };

  $("pf-opendir").onclick = () => B.OpenFolder("");

  window.loadPrefs = async () => {
    try {
      const data = await B.GetSurvey();
      survey.load(data);
      base = survey.dump();
      keywords = data.keywords || [];
      renderKeywords();
      $("pf-ctx").checked = !data.disableCtx;
      setStatus($("pf-status"), "");
    } catch (e) {
      setStatus($("pf-status"), "" + e, "err");
    }
  };
};
