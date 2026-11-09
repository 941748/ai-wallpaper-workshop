// 设置视图: 当前壁纸页 + 偏好页

// ================= 当前壁纸 =================
window.initCurrent = function () {
  const $ = (id) => document.getElementById(id);

  $("cur-like").onclick = () => rate("liked", "喜欢");
  $("cur-dislike").onclick = () => rate("disliked", "不喜欢");
  $("cur-swap").onclick = swapNow;

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
    setStatus($("cur-status"), "正在出图…(约 1-2 分钟, 完成后自动生效)", "hint");
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
    }
    btn.disabled = false;
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

  $("pf-reinit").onclick = async () => {
    const ok = await confirmBox("重新初始化向导",
      "将重新运行初始化向导并重新勾选偏好问卷。\n这会被记为一次偏好漂移依据, 帮助后续出图更贴近你。\n继续吗?");
    if (!ok) return;
    try {
      await B.RestartWizard();
      location.reload();
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
