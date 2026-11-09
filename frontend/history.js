// 历史页: 本地/云端双模式 — 5 点需求实现
// ①列表行=心形+时间+规格(无提示词, 选中后预览下方显示全文)
// ②点行右侧预览(Go 侧已修 image.Decode 自动识别)
// ③云端视觉区分: ☁ 前缀+状态条+按钮文案「召回到本地并应用」+切换后自动预览首张
// ④全部召回: {jobID}.png 命名去重, 进度条, 服务端 offset 翻页
// ⑤收藏常可用
// ⑥本地行收藏星标+只看收藏筛选(与安卓画廊一致): {jobID}.png 反查 + 服务端收藏集合 GetFavs

window.initHistory = function () {
  const $ = (id) => document.getElementById(id);
  let mode = "local";          // local | cloud
  let items = [];              // 当前列表数据(本地模式 = 视图, 见 localAll/favOnly)
  let selected = -1;
  // 云端列表会话缓存(stale-while-revalidate): 切回/刷新时先秒显上次结果再后台拉新。
  // 与 items 指向同一数组 → 收藏/召回的就地修改自动同步进缓存。
  let cloudCache = null;
  let previewSeq = 0;          // 防过期预览串号
  let recalling = false;
  // 本地收藏: 收藏态存服务端 archive 表, {jobID}.png 反查关联(旧时间戳文件无法关联, 不入筛选);
  // favOnly 只作用于本地列表(云端列表已有收藏置顶+♥按钮)。
  let favSet = new Set();
  let favOnly = false;
  let localAll = null;         // 本地全量(未筛选), 切筛选时的数据源

  // ---------- 列表渲染 ----------
  function renderList() {
    const root = $("hist-list");
    root.innerHTML = "";
    items.forEach((it, i) => {
      const row = document.createElement("div");
      row.className = "hist-row" + (i === selected ? " active" : "");
      if (mode === "cloud") {
        const heart = it.fav ? "♥" : "♡";
        row.innerHTML =
          '<span class="heart' + (it.fav ? " on" : "") + '">' + heart + "</span>" +
          '<span class="meta">☁ ' + it.createdAt + "</span>" +
          '<span class="spec">' + it.width + "×" + it.height + "</span>" +
          (it.hasLocal ? '<span class="spec" title="本地已有">✓</span>' : "");
      } else {
        const label = it.jobId
          ? it.at + "  " + it.jobId.slice(0, 8)
          : it.name.replace(/^wallpaper_/, "").replace(/\.png$/, "") + "  (" + it.at + ")";
        const fav = localFavOf(it);
        row.innerHTML =
          '<span class="heart' + (fav ? " on" : "") + '">' + (fav ? "♥" : "♡") + "</span>" +
          '<span class="meta">' + label + "</span>" +
          (it.jobId ? '<span class="spec">云端可取</span>' : "");
        const heart = row.querySelector(".heart");
        heart.onclick = (ev) => { ev.stopPropagation(); toggleLocalFav(it); };
      }
      row.onclick = () => select(i);
      root.appendChild(row);
    });
    if (items.length === 0) {
      const emptyMsg = favOnly && mode === "local"
        ? "还没有收藏的图片 — 点行内 ♡ 收藏一张"
        : "暂无记录";
      root.innerHTML = '<div class="muted" style="padding:20px;text-align:center">' + emptyMsg + "</div>";
    }
  }

  // 本地图收藏判定: jobId 行看云端收藏集合(跨端同步), 旧时间戳行看本地清单(它.fav)。
  const localFavOf = (x) => !!(x.fav || (x.jobId && favSet.has(x.jobId)));

  // 本地行星标(全行可标, 与安卓画廊一致): jobId→云端, 旧时间戳文件→本地清单; 乐观翻心, 失败回滚。
  async function toggleLocalFav(it) {
    const next = !localFavOf(it);
    const prevLocal = it.fav;
    const prevCloud = it.jobId ? favSet.has(it.jobId) : false;
    if (it.jobId) {
      if (next) favSet.add(it.jobId); else favSet.delete(it.jobId);
    } else {
      it.fav = next;
    }
    renderList();
    try {
      if (it.jobId) await B.ToggleFav(it.jobId, next);
      else await B.SetLocalFav(it.name, next);
      setStatus($("hist-status"), next ? "已收藏 ♥" : "已取消收藏", "ok");
      if (favOnly) rebuildLocalView(next ? it : null); // 筛选中: 收藏进入/取消移出即时生效
    } catch (e) {
      if (it.jobId) {
        if (prevCloud) favSet.add(it.jobId); else favSet.delete(it.jobId);
      } else {
        it.fav = prevLocal;
      }
      renderList();
      setStatus($("hist-status"), "" + e, "err");
    }
  }

  // 本地视图重建(favOnly 筛选); keep = 尽量保留当前选中项。
  function rebuildLocalView(keep) {
    const all = localAll || [];
    items = favOnly ? all.filter(localFavOf) : all;
    selected = keep ? items.indexOf(keep) : -1;
    renderList();
  }

  // ---------- 选中 + 预览 ----------
  async function select(i) {
    selected = i;
    renderList();
    const seq = ++previewSeq;
    const it = items[i];
    $("hist-empty").classList.remove("hidden");
    $("hist-empty").textContent = "加载预览…";
    $("hist-img").classList.add("hidden");
    setStatus($("hist-status"), "");
    try {
      let b64;
      if (mode === "cloud") {
        b64 = await B.PreviewCloud(it.jobId);
      } else {
        b64 = await B.PreviewLocal(it.path);
      }
      if (seq !== previewSeq) return; // 已换选中, 过期丢弃
      $("hist-img").src = "data:image/jpeg;base64," + b64;
      $("hist-img").classList.remove("hidden");
      $("hist-empty").classList.add("hidden");
    } catch (e) {
      if (seq !== previewSeq) return;
      $("hist-empty").textContent = "预览加载失败";
      setStatus($("hist-status"), "" + e, "err");
    }
  }

  // ---------- 数据加载 ----------
  async function loadLocal() {
    localAll = await B.GetLocalHistory();
    items = localAll;
    selected = -1;
    renderList();
    $("hist-empty").textContent = "选择左侧图片预览";
    // 星标显示需要收藏集合(先显示列表, 收藏态返回后纠偏; 失败保留已有缓存)
    try {
      const ids = await B.GetFavs();
      favSet = new Set(ids || []);
      if (mode === "local") {
        const keep = selected >= 0 && items[selected] ? items[selected] : null;
        rebuildLocalView(keep);
      }
    } catch (e) {
      if ((localAll || []).some((x) => x.jobId)) {
        setStatus($("hist-status"), "收藏状态未同步: " + e, "hint");
      }
    }
  }

  async function loadCloud(autoPreview) {
    const hadStale = cloudCache != null;
    // 记住当前选中(按 jobId, 刷新重排后仍能对上)
    const keepId = selected >= 0 && items[selected] ? items[selected].jobId : null;

    if (hadStale) {
      // stale: 缓存秒显, 不打断用户, 后台静默拉新
      items = cloudCache;
      selected = keepId ? items.findIndex((x) => x.jobId === keepId) : -1;
      renderList();
      setStatus($("hist-status"), "");
      if (selected < 0 && autoPreview && items.length > 0) select(0);
      else if (items.length === 0) $("hist-empty").textContent = "暂无记录";
    } else {
      setStatus($("hist-status"), "加载中…", "hint");
    }

    const r = await B.GetCloudHistory(0);
    if (!r.ok) {
      if (hadStale) {
        // 有缓存: 保留 stale 显示, 只提示刷新失败
        setStatus($("hist-status"), "刷新失败, 仍显示上次结果: " + (r.message || "网络错误"), "err");
        return true;
      }
      // 无缓存失败: 清空列表, 不能让本地行留在云端模式下造成"错行"混淆
      items = [];
      selected = -1;
      renderList();
      $("hist-empty").textContent = "云端历史加载失败";
      $("hist-img").classList.add("hidden");
      $("hist-empty").classList.remove("hidden");
      setStatus($("hist-status"), r.message || "云端历史加载失败", "err");
      return false;
    }

    // revalidate: 新数据原位替换, 保持选中(收藏置顶重排也能对上)
    cloudCache = r.items;
    items = r.items;
    selected = keepId ? items.findIndex((x) => x.jobId === keepId) : -1;
    renderList();
    if (!hadStale) {
      // 首次加载: ③ 自动预览首张
      if (autoPreview && items.length > 0) select(0);
      else $("hist-empty").textContent = "选择左侧图片预览";
    } else if (selected < 0 && autoPreview && items.length > 0) {
      select(0); // 原选中已被远端删除等 → 兜底预览首张
    } else if (selected >= 0) {
      setStatus($("hist-status"), ""); // 清掉可能残留的刷新失败提示
    }
    return true;
  }

  function setMode(m) {
    mode = m;
    const cloud = m === "cloud";
    $("hist-toggle").textContent = cloud ? "本地历史" : "云端历史";
    $("hist-modebar").classList.toggle("hidden", !cloud);
    // ③ 按钮文案切换
    $("hist-apply").textContent = cloud ? "召回到本地并应用" : "重新应用这张";
    $("hist-fav").style.display = cloud ? "" : "none";
    $("hist-favonly").style.display = cloud ? "none" : "";
    $("hist-del").style.display = cloud ? "none" : "";
    $("hist-recallall").style.display = cloud ? "" : "none";
    // 切模式重置筛选, 避免"列表缺图"式困惑
    favOnly = false;
    $("hist-favonly").textContent = "♡ 只看收藏";
    setStatus($("hist-status"), "");
  }

  // ---------- 按钮 ----------
  $("hist-toggle").onclick = async () => {
    if (mode === "cloud") {
      setMode("local");
      await loadLocal();
    } else {
      setMode("cloud");
      await loadCloud(true); // 缓存秒显+后台刷新; 状态文案由 loadCloud 自管
    }
  };

  $("hist-refresh").onclick = async () => {
    setStatus($("hist-status"), "刷新中…", "hint");
    if (mode === "cloud") await loadCloud(false);
    else await loadLocal();
    setStatus($("hist-status"), "");
  };

  $("hist-apply").onclick = async () => {
    if (selected < 0 || !items[selected]) {
      setStatus($("hist-status"), "请先选择一张图片。", "hint");
      return;
    }
    const it = items[selected];
    const btn = $("hist-apply");
    btn.disabled = true;
    setStatus($("hist-status"), mode === "cloud" ? "召回中…" : "应用中…", "hint");
    try {
      let msg;
      if (mode === "cloud") {
        const r = await B.RecallCloud(it.jobId);
        msg = r.message;
        if (r.ok) {
          it.hasLocal = true;
          renderList();
          await loadLocalOnce();
        }
      } else {
        msg = await B.ApplyLocal(it.path);
      }
      setStatus($("hist-status"), msg, "ok");
    } catch (e) {
      setStatus($("hist-status"), "" + e, "err");
    }
    btn.disabled = false;
  };

  async function loadLocalOnce() { /* 召回后仅在切回本地时刷新, 这里保持云端列表不动 */ }

  // ④ 全部召回
  $("hist-recallall").onclick = async () => {
    if (recalling) return;
    const ok = await confirmBox("全部召回",
      "将把云端全部本地图拉回本地(已有 {jobID}.png 的跳过)。\n" +
      "只下载留档, 不更换当前壁纸。\n\n继续吗?");
    if (!ok) return;
    recalling = true;
    const btn = $("hist-recallall");
    btn.disabled = true;
    $("hist-progress").classList.remove("hidden");
    setStatus($("hist-status"), "召回中…", "hint");
    // 进度事件(Wails 事件经 runtime.EventsOn 投递, 详见 settings.js)
    const onProg = (p) => {
      const pct = p.total > 0 ? Math.round(((p.done + p.skipped) * 100) / p.total) : 0;
      $("hist-progress-bar").style.width = Math.min(pct, 95) + "%";
      setStatus($("hist-status"),
        "召回中… 已下载 " + p.done + " · 跳过 " + p.skipped + " · 失败 " + p.failed, "hint");
    };
    const offProg = window.runtime.EventsOn("recall-progress", onProg);
    try {
      const r = await B.RecallAll();
      $("hist-progress-bar").style.width = "100%";
      setStatus($("hist-status"), r.message, r.ok ? "ok" : "hint");
      if (mode === "cloud") await loadCloud(false);
    } catch (e) {
      setStatus($("hist-status"), "" + e, "err");
    } finally {
      offProg();
      setTimeout(() => {
        $("hist-progress").classList.add("hidden");
        $("hist-progress-bar").style.width = "0%";
      }, 800);
      btn.disabled = false;
      recalling = false;
    }
  };

  // ⑦ 删除本地图(与安卓长按删除一致): 确认 → jobId 行同步清云端副本 → 删本地 → 两处缓存同步
  $("hist-del").onclick = async () => {
    if (mode !== "local") {
      setStatus($("hist-status"), "云端图片请先召回到本地, 再在本地历史删除。", "hint");
      return;
    }
    if (selected < 0 || !items[selected]) {
      setStatus($("hist-status"), "请先选择一张图片。", "hint");
      return;
    }
    const it = items[selected];
    const ok = await confirmBox(
      "删除这张图片？",
      it.jobId
        ? "将从「最近生成」中移除并清理云端副本, 删除后无法恢复。已设置的壁纸不受影响。"
        : "将从「最近生成」中移除, 删除后无法恢复。已设置的壁纸不受影响。"
    );
    if (!ok) return;
    try {
      const msg = await B.DeleteLocal(it.name);
      setStatus($("hist-status"), msg, "ok");
      localAll = (localAll || []).filter((x) => x.name !== it.name);
      if (cloudCache && it.jobId) cloudCache = cloudCache.filter((x) => x.jobId !== it.jobId);
      rebuildLocalView();
      if (items.length === 0) $("hist-empty").textContent = "暂无记录";
    } catch (e) {
      setStatus($("hist-status"), "" + e, "err");
    }
  };

  // 只看收藏(本地模式, 与安卓画廊"收藏中"筛选一致)
  $("hist-favonly").onclick = () => {
    if (mode !== "local") return;
    favOnly = !favOnly;
    $("hist-favonly").textContent = favOnly ? "♥ 只看收藏中" : "♡ 只看收藏";
    rebuildLocalView();
    setStatus($("hist-status"), favOnly ? "只显示已收藏 ♥ 的图片" : "", favOnly ? "hint" : "");
    if (favOnly && items.length > 0) select(0);
  };

  // ⑤ 收藏(常可用)
  $("hist-fav").onclick = async () => {
    if (mode !== "cloud") {
      setStatus($("hist-status"), "请先切换到云端历史。", "hint");
      return;
    }
    if (selected < 0 || !items[selected]) {
      setStatus($("hist-status"), "请先选择一张图片。", "hint");
      return;
    }
    const it = items[selected];
    const next = !it.fav;
    it.fav = next; // 乐观翻心
    renderList();
    try {
      const fav = await B.ToggleFav(it.jobId, next);
      it.fav = fav;
      renderList();
      setStatus($("hist-status"), fav ? "已收藏 ♥" : "已取消收藏", "ok");
      // 置顶重排
      items.sort((a, b) => (b.fav ? 1 : 0) - (a.fav ? 1 : 0));
      selected = items.indexOf(it);
      renderList();
    } catch (e) {
      it.fav = !next; // 回滚
      renderList();
      setStatus($("hist-status"), "" + e, "err");
    }
  };

  $("hist-open").onclick = () => B.OpenFolder(window.__boot ? window.__boot.dir + "/wallpapers" : "");

  // ---------- 入口 ----------
  window.loadHistory = async function () {
    if (mode === "cloud") await loadCloud(false);
    else await loadLocal();
  };
  setMode("local");
  window.loadHistory();
};
