package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"wallpaper/cloud/store"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
)

//go:embed echarts.min.js
var echartsJS []byte

// ---------- 管理看板(2026-11-06 P0 改版: 分页签 + ECharts 自托管) ----------

var adminTmpl = template.Must(template.New("admin").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.App}} 管理看板</title>
<style>
:root{--bg:#0f172a;--card:#1e293b;--line:#334155;--fg:#e2e8f0;--muted:#94a3b8;--acc:#7dd3fc}
*{box-sizing:border-box}
body{font-family:system-ui;margin:0;background:var(--bg);color:var(--fg)}
header{display:flex;align-items:center;gap:18px;padding:12px 22px;border-bottom:1px solid var(--line);position:sticky;top:0;background:var(--bg);z-index:10;flex-wrap:wrap}
h1{font-size:15px;margin:0;font-weight:600}
nav{display:flex;gap:4px}
nav button{background:none;border:none;color:var(--muted);padding:8px 15px;font-size:14px;cursor:pointer;border-radius:8px}
nav button.on{background:var(--card);color:var(--acc);font-weight:600}
main{padding:18px 22px;max-width:1460px;margin:0 auto}
section{display:none}section.on{display:block}
h2{font-size:13px;color:var(--muted);margin:22px 0 10px;font-weight:600}
.note{font-size:12px;color:#64748b;margin:-4px 0 8px}
.kpis{display:flex;flex-wrap:wrap;gap:10px}
.kpi{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:12px 16px;min-width:118px}
.kpi .v{font-size:20px;font-weight:700;margin-top:3px}
.kpi .l{font-size:12px;color:var(--muted)}
.charts{display:grid;grid-template-columns:repeat(auto-fit,minmax(430px,1fr));gap:12px}
.chart{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:12px 14px}
.chart h3{margin:0 0 4px;font-size:13px;color:var(--muted);font-weight:600}
.chart .box{height:270px}
.scroll{max-height:560px;overflow:auto;border:1px solid var(--line);border-radius:12px}
table{border-collapse:collapse;width:100%;font-size:12.5px}
td,th{border-bottom:1px solid var(--line);padding:7px 10px;text-align:left;vertical-align:top}
th{background:#243447;color:var(--muted);font-weight:600;position:sticky;top:0;z-index:1}
tr:last-child td{border-bottom:none}
tbody tr:hover{background:#1a2740}
code{color:var(--acc)}
.p{white-space:pre-wrap;word-break:break-all;max-width:440px}
.mut{color:#64748b}.top{color:#4ade80}.bad{color:#f87171}
</style></head><body>
<header>
<h1>{{.App}} 云服务 · v{{.Version}} · 管理看板</h1>
<nav>
<button class="on" data-t="ov">概览</button>
<button data-t="us">用户</button>
<button data-t="pf">偏好分析</button>
<button data-t="gn">出图</button>
<button data-t="sy">系统</button>
</nav>
</header>
<main>

<section id="ov" class="on">
<div class="kpis">
<div class="kpi"><div class="l">总用户</div><div class="v">{{.KUsers}}</div></div>
<div class="kpi"><div class="l">7 日新增</div><div class="v">{{.KNew7}}</div></div>
<div class="kpi"><div class="l">今日出图</div><div class="v">{{.KTodayGen}}</div></div>
<div class="kpi"><div class="l">今日成功</div><div class="v">{{.KTodayOK}}</div></div>
<div class="kpi"><div class="l">出图成功率</div><div class="v">{{.KRate}}</div></div>
<div class="kpi"><div class="l">官网 PV</div><div class="v">{{.KPV}}</div></div>
<div class="kpi"><div class="l">官网 UV</div><div class="v">{{.KUV}}</div></div>
<div class="kpi"><div class="l">客户端下载</div><div class="v">{{.KDL}}</div></div>
<div class="kpi"><div class="l">APK 下载</div><div class="v">{{.KApkDL}}</div></div>
<div class="kpi"><div class="l">图片下载</div><div class="v">{{.KImgDL}}</div></div>
</div>
<h2>趋势</h2>
<div class="charts">
<div class="chart"><h3>每日出图(近 30 天, 全站聚合)</h3><div class="box" id="cGen"></div></div>
<div class="chart"><h3>用户增长(累计)</h3><div class="box" id="cGrow"></div></div>
<div class="chart"><h3>官网访问(近 30 天)</h3><div class="box" id="cSite"></div></div>
</div>
</section>

<section id="us">
<h2>用户偏好画像({{len .UserRows}})</h2>
<p class="note">风格包=画像 packs 字段(空=旧档未设置按权重推断); 五维 Top=画像中未点踩的最高权重标签(并列全显, 最多 5 个); 点踩数=避让清单词条数。按最近出图倒序。</p>
<div class="scroll"><table>
<tr><th>user_id</th><th>版本</th><th>注册</th><th>最近出图</th><th>出图数</th><th>风格包</th><th>风格 Top</th><th>题材 Top</th><th>色调 Top</th><th>情绪 Top</th><th>构图 Top</th><th>点踩</th></tr>
{{range .UserRows}}<tr>
<td><code>{{.UserID}}</code>{{if .Disabled}} <span class="bad">禁用</span>{{end}}</td>
<td>{{if .Ver}}{{.Ver}}{{else}}<span class="mut">—</span>{{end}}</td>
<td>{{.CreatedAt}}</td><td>{{if .LastAt}}{{.LastAt}}{{else}}<span class="mut">—</span>{{end}}</td>
<td>{{.PromptCount}}</td><td>{{.PackCN}}</td>
<td class="top">{{.Style}}</td><td class="top">{{.Subject}}</td><td class="top">{{.Palette}}</td><td class="top">{{.Mood}}</td><td class="top">{{.Comp}}</td>
<td>{{if .DislikeN}}<span class="bad">{{.DislikeN}}</span>{{else}}0{{end}}</td>
</tr>{{end}}
</table></div>
</section>

<section id="pf">
<h2>偏好分析(优化方向)</h2>
<p class="note">声明=画像快照(用户显式选择, 全部用户聚合); 实际=近 30 天成功出图的组合频次(一图一包采样结果); 点踩=避让清单跨用户计数。</p>
<div class="charts">
<div class="chart"><h3>风格包用户分布(声明)</h3><div class="box" id="cPacks"></div></div>
<div class="chart"><h3>点踩词条 Top10(跨用户)</h3><div class="box" id="cDis"></div></div>
<div class="chart"><h3>实际出图 · 题材 Top10(近 30 天)</h3><div class="box" id="cUseSj"></div></div>
<div class="chart"><h3>实际出图 · 风格 Top10(近 30 天)</h3><div class="box" id="cUseSt"></div></div>
</div>
<h2>声明喜欢 · 分维度 Top10(跨用户计数)</h2>
<div class="charts">
<div class="chart"><h3>风格</h3><div class="box" id="cLkStyle"></div></div>
<div class="chart"><h3>题材</h3><div class="box" id="cLkSj"></div></div>
<div class="chart"><h3>色调</h3><div class="box" id="cLkPal"></div></div>
<div class="chart"><h3>情绪</h3><div class="box" id="cLkMood"></div></div>
<div class="chart"><h3>构图</h3><div class="box" id="cLkComp"></div></div>
</div>
</section>

<section id="gn">
<h2>出图统计</h2>
<div class="charts">
<div class="chart"><h3>每日出图按来源(近 30 天)</h3><div class="box" id="cSrc"></div></div>
<div class="chart"><h3>分辨率 Top10(近 30 天成功)</h3><div class="box" id="cRes"></div></div>
</div>
<h2>最近出图记录(20, 提示词完整)</h2>
<div class="scroll"><table>
<tr><th>时间</th><th>user</th><th>来源</th><th>工作流</th><th>尺寸</th><th>成功</th><th>正向提示词(完整)</th><th>负向提示词(完整)</th></tr>
{{range .Prompts}}<tr><td>{{.At}}</td><td><code>{{.User}}</code></td><td>{{.Source}}</td><td>{{.WorkflowID}}</td><td>{{.W}}x{{.H}}</td><td>{{.Success}}</td><td class="p">{{.Positive}}</td><td class="p">{{.Negative}}</td></tr>{{end}}
</table></div>
<h2>最近漂移/评价事件(30)</h2>
<div class="scroll"><table>
<tr><th>时间</th><th>user</th><th>类型</th><th>详情</th></tr>
{{range .Signals}}<tr><td>{{.At}}</td><td><code>{{.User}}</code></td><td>{{.Type}}</td><td>{{.Detail}}</td></tr>{{end}}
</table></div>
</section>

<section id="sy">
<div class="kpis">
<div class="kpi"><div class="l">schema 版本</div><div class="v">{{.KSchema}}</div></div>
<div class="kpi"><div class="l">预生成池(就绪/总)</div><div class="v">{{.KPregen}}</div></div>
<div class="kpi"><div class="l">配额(出图 时/天)</div><div class="v" style="font-size:15px">{{.KQuotaG}}</div></div>
<div class="kpi"><div class="l">配额(LLM 时/天)</div><div class="v" style="font-size:15px">{{.KQuotaL}}</div></div>
</div>
<h2>用量(近 7 天)</h2>
<div class="charts"><div class="chart"><h3>LLM / 出图 调用次数</h3><div class="box" id="cUsage"></div></div>
<div class="chart"><h3>客户端版本分布(更新检查上报, 按用户数)</h3><div class="box" id="cVer"></div></div></div>
<h2>设备({{len .Devices}}, 注册/配对上报)</h2>
<div class="scroll"><table>
<tr><th>device</th><th>类型</th><th>型号/系统</th><th>应用版本</th><th>屏幕</th><th>user</th><th>时间</th></tr>
{{range .Devices}}<tr><td>{{.Device}}</td><td>{{if .Type}}{{.Type}}{{else}}<span class="mut">—</span>{{end}}</td>
<td>{{.Model}}{{if .OS}} {{.OS}} {{.OSVersion}}{{end}}</td><td>{{if .AppVer}}{{.AppVer}}{{else}}<span class="mut">—</span>{{end}}</td>
<td>{{if .ScreenW}}{{.ScreenW}}x{{.ScreenH}}{{else}}<span class="mut">—</span>{{end}}</td>
<td><code>{{.UserID}}</code></td><td>{{.CreatedAt}}</td></tr>{{end}}
</table></div>
<h2>任务队列({{len .Jobs}})</h2>
<div class="scroll"><table>
<tr><th>job_id</th><th>kind</th><th>priority</th><th>status</th><th>error</th></tr>
{{range .Jobs}}<tr><td><code>{{.JobID}}</code></td><td>{{.Kind}}</td><td>{{.Priority}}</td><td>{{.Status}}</td><td>{{.Error}}</td></tr>{{end}}
</table></div>
<h2>最近官网访问事件(20, 含 IP)</h2>
<div class="scroll"><table>
<tr><th>时间</th><th>类型</th><th>IP</th><th>标识(哈希/user)</th></tr>
{{range .SiteEvents}}<tr><td>{{.TS}}</td><td>{{.Kind}}</td><td>{{.IP}}</td><td><code>{{.IPh}}</code></td></tr>{{end}}
</table></div>
</section>

</main>
<script src="/admin/echarts.min.js"></script>
<script>
const D = {{.DataJS}};
const ACC = ['#7dd3fc','#4ade80','#fbbf24','#f472b6','#a78bfa','#fb923c','#22d3ee','#e879f9','#34d399','#f87171'];
const AX = {axisLine:{lineStyle:{color:'#334155'}},axisLabel:{color:'#94a3b8',fontSize:11},splitLine:{lineStyle:{color:'#1e293b'}}};
const TT = {backgroundColor:'#0f172a',borderColor:'#334155',textStyle:{color:'#e2e8f0',fontSize:12}};
const LG = {textStyle:{color:'#94a3b8',fontSize:11}};
const CH = {};
function draw(id, opt) {
  if (CH[id]) return;
  const elm = document.getElementById(id);
  if (!elm) return;
  CH[id] = echarts.init(elm);
  CH[id].setOption(opt);
}
function lineChart(id, days, series) {
  draw(id, {
    color: ACC,
    tooltip: Object.assign({trigger:'axis'}, TT),
    legend: Object.assign({top:0, data:series.map(s=>s.name)}, LG),
    grid: {left:8, right:14, top:36, bottom:6, containLabel:true},
    xAxis: Object.assign({type:'category', data:days, boundaryGap:false}, AX),
    yAxis: Object.assign({type:'value'}, AX),
    series: series.map(s=>({name:s.name, type:'line', data:s.data, smooth:true, symbol:'none',
      lineStyle:{width:2}, areaStyle: s.area ? {opacity:0.14} : undefined}))
  });
}
function hbar(id, pairs, color) {
  const labels = pairs.map(p=>p[0]).slice().reverse();
  const vals = pairs.map(p=>p[1]).slice().reverse();
  draw(id, {
    tooltip: Object.assign({trigger:'axis', axisPointer:{type:'shadow'}}, TT),
    grid: {left:8, right:36, top:8, bottom:6, containLabel:true},
    xAxis: Object.assign({type:'value'}, AX),
    yAxis: Object.assign({type:'category', data:labels}, AX),
    series: [{type:'bar', data:vals, barMaxWidth:15,
      itemStyle:{color:color || ACC[0], borderRadius:[0,6,6,0]}}]
  });
}
function drawOv() {
  const g = D.gen_daily || [];
  lineChart('cGen', g.map(r=>r[0]), [
    {name:'出图', data:g.map(r=>r[1]), area:true},
    {name:'成功', data:g.map(r=>r[2])}
  ]);
  const gr = D.growth || [];
  let acc = 0;
  const cum = gr.map(r=>(acc += r[1]));
  lineChart('cGrow', gr.map(r=>r[0]), [{name:'累计用户', data:cum, area:true}]);
  const st = D.site_daily || [];
  lineChart('cSite', st.map(r=>r[0]), [
    {name:'PV', data:st.map(r=>r[1])},
    {name:'UV', data:st.map(r=>r[2]), area:true},
    {name:'客户端下载', data:st.map(r=>r[3])}
  ]);
}
function drawPf() {
  hbar('cPacks', D.packs || [], ACC[0]);
  hbar('cDis', D.dislikes || [], '#f87171');
  hbar('cUseSj', (D.use && D.use.subject) || [], ACC[2]);
  hbar('cUseSt', (D.use && D.use.style) || [], ACC[2]);
  const lk = D.likes || {};
  hbar('cLkStyle', lk.style || [], ACC[1]);
  hbar('cLkSj', lk.subject || [], ACC[1]);
  hbar('cLkPal', lk.palette || [], ACC[1]);
  hbar('cLkMood', lk.mood || [], ACC[1]);
  hbar('cLkComp', lk.composition || [], ACC[1]);
}
function drawGn() {
  const src = D.src || {days:[], series:{}};
  const names = Object.keys(src.series || {});
  draw('cSrc', {
    color: ACC,
    tooltip: Object.assign({trigger:'axis', axisPointer:{type:'shadow'}}, TT),
    legend: Object.assign({top:0, data:names}, LG),
    grid: {left:8, right:14, top:36, bottom:6, containLabel:true},
    xAxis: Object.assign({type:'category', data:src.days}, AX),
    yAxis: Object.assign({type:'value'}, AX),
    series: names.map(n=>({name:n, type:'bar', stack:'t', data:src.series[n], barMaxWidth:18}))
  });
  hbar('cRes', D.res || [], ACC[3]);
}
function drawSy() {
  const u = D.usage || {days:[], llm:[], gen:[]};
  draw('cUsage', {
    color: ACC,
    tooltip: Object.assign({trigger:'axis', axisPointer:{type:'shadow'}}, TT),
    legend: Object.assign({top:0, data:['LLM','出图']}, LG),
    grid: {left:8, right:14, top:36, bottom:6, containLabel:true},
    xAxis: Object.assign({type:'category', data:u.days}, AX),
    yAxis: Object.assign({type:'value'}, AX),
    series: [
      {name:'LLM', type:'bar', data:u.llm, barMaxWidth:16},
      {name:'出图', type:'bar', data:u.gen, barMaxWidth:16}
    ]
  });
  hbar('cVer', D.clientver || [], ACC[4]);
}
const TABDRAW = {ov:drawOv, us:function(){}, pf:drawPf, gn:drawGn, sy:drawSy};
document.querySelectorAll('nav button').forEach(b=>{
  b.onclick = ()=>{
    document.querySelectorAll('nav button').forEach(x=>x.classList.remove('on'));
    b.classList.add('on');
    document.querySelectorAll('main section').forEach(s=>s.classList.remove('on'));
    document.getElementById(b.dataset.t).classList.add('on');
    (TABDRAW[b.dataset.t] || function(){})();
    requestAnimationFrame(()=>{ for (const k in CH) CH[k].resize(); });
  };
});
window.addEventListener('resize', ()=>{ for (const k in CH) CH[k].resize(); });
</script>
</body></html>`))

// ---------- 数据组装 ----------

type userTblRow struct {
	UserID, CreatedAt, LastAt, PackCN   string
	PromptCount                         int64
	Style, Subject, Palette, Mood, Comp string
	Ver                                 string
	DislikeN                            int
	Disabled                            bool
}

// cnLabel 维度值中文名(词典缺失回退英文键)。
func cnLabel(dim, val string) string {
	if val == "" {
		return "—"
	}
	if v := taxonomy.ValueOf(dim, val); v != nil && v.NameCN != "" {
		return v.NameCN
	}
	return val
}

// dimTops 解析画像:各维度"未点踩的最高权重"标签(并列全显, 最多 5 个 + 溢出计数)。无有效值返回 "—"。
func dimTops(profileJSON, dislikedJSON string) map[string]string {
	out := map[string]string{}
	for _, d := range taxonomy.Dimensions {
		out[d.ID] = "—"
	}
	if profileJSON == "" {
		return out
	}
	var w map[string]float64
	if err := json.Unmarshal([]byte(profileJSON), &w); err != nil {
		return out
	}
	dis := map[string]bool{}
	if dislikedJSON != "" {
		var arr []string
		if err := json.Unmarshal([]byte(dislikedJSON), &arr); err == nil {
			for _, k := range arr {
				dis[k] = true
			}
		}
	}
	for _, d := range taxonomy.Dimensions {
		prefix := d.ID + "/"
		maxW := 0.0
		var tops []string
		for k, v := range w {
			if !strings.HasPrefix(k, prefix) || dis[k] {
				continue
			}
			if v > maxW+1e-9 {
				maxW, tops = v, []string{strings.TrimPrefix(k, prefix)}
			} else if maxW > 0 && v >= maxW-1e-9 {
				tops = append(tops, strings.TrimPrefix(k, prefix))
			}
		}
		if len(tops) == 0 {
			continue
		}
		sort.Strings(tops)
		names := make([]string, 0, len(tops))
		for i, t := range tops {
			if i >= 5 {
				names = append(names, fmt.Sprintf("+%d", len(tops)-5))
				break
			}
			names = append(names, cnLabel(d.ID, t))
		}
		out[d.ID] = strings.Join(names, " / ")
	}
	return out
}

// packCN 风格包字段转展示文本。
func packCN(packsJSON string) string {
	if packsJSON == "" {
		return "未设置(推断)"
	}
	if packsJSON == "[]" {
		return "显式清空"
	}
	var arr []string
	if err := json.Unmarshal([]byte(packsJSON), &arr); err != nil || len(arr) == 0 {
		return "未设置(推断)"
	}
	return strings.Join(arr, "、")
}

// topN map 计数取前 n(降序, 同数按 key 升序), 返回 [label,count] 对。
func topN(m map[string]int, n int) [][2]any {
	type kv struct {
		k string
		v int
	}
	arr := make([]kv, 0, len(m))
	for k, v := range m {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].v != arr[j].v {
			return arr[i].v > arr[j].v
		}
		return arr[i].k < arr[j].k
	})
	if len(arr) > n {
		arr = arr[:n]
	}
	out := make([][2]any, 0, len(arr))
	for _, e := range arr {
		out = append(out, [2]any{e.k, e.v})
	}
	return out
}

// buildPrefAgg 聚合全部用户画像:风格包分布 / 各维声明喜欢计数 / 点踩计数。
func buildPrefAgg(rows []store.UserPrefRow) (packs map[string]int, likes map[string]map[string]int, dislikes map[string]int) {
	packs = map[string]int{}
	likes = map[string]map[string]int{}
	dislikes = map[string]int{}
	for _, d := range taxonomy.Dimensions {
		likes[d.ID] = map[string]int{}
	}
	for _, r := range rows {
		// 风格包
		if r.Packs == "" {
			packs["未设置(推断)"]++
		} else if r.Packs == "[]" {
			packs["显式清空"]++
		} else {
			var arr []string
			if err := json.Unmarshal([]byte(r.Packs), &arr); err == nil {
				for _, p := range arr {
					packs[p]++
				}
			}
		}
		if r.Profile == "" {
			continue
		}
		var w map[string]float64
		if err := json.Unmarshal([]byte(r.Profile), &w); err != nil {
			continue
		}
		dis := map[string]bool{}
		if r.Disliked != "" {
			var arr []string
			if err := json.Unmarshal([]byte(r.Disliked), &arr); err == nil {
				for _, k := range arr {
					dis[k] = true
				}
			}
		}
		// 点踩(按词条计数, 键形如 dim/val)
		for k := range dis {
			dim, val, ok := strings.Cut(k, "/")
			if !ok {
				continue
			}
			dislikes[cnLabel(dim, val)]++
		}
		// 声明喜欢(权重达 LikeWeight 且不在点踩清单)
		for k, v := range w {
			if v < survey.LikeWeight-1e-9 {
				continue
			}
			dim, val, ok := strings.Cut(k, "/")
			if !ok || dis[k] {
				continue
			}
			if _, ok := likes[dim]; ok {
				likes[dim][cnLabel(dim, val)]++
			}
		}
	}
	return
}

// buildComboUse 近 30 天出图组合按题材/风格聚合。
func buildComboUse(rows []store.ComboCountRow) (subj, style map[string]int64) {
	subj, style = map[string]int64{}, map[string]int64{}
	for _, r := range rows {
		var m map[string]string
		if err := json.Unmarshal([]byte(r.Combo), &m); err != nil {
			continue
		}
		if v := m["subject"]; v != "" {
			subj[cnLabel("subject", v)] += r.N
		}
		if v := m["style"]; v != "" {
			style[cnLabel("style", v)] += r.N
		}
	}
	return
}

// topN64 int64 版 topN。
func topN64(m map[string]int64, n int) [][2]any {
	type kv struct {
		k string
		v int64
	}
	arr := make([]kv, 0, len(m))
	for k, v := range m {
		arr = append(arr, kv{k, v})
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].v != arr[j].v {
			return arr[i].v > arr[j].v
		}
		return arr[i].k < arr[j].k
	})
	if len(arr) > n {
		arr = arr[:n]
	}
	out := make([][2]any, 0, len(arr))
	for _, e := range arr {
		out = append(out, [2]any{e.k, e.v})
	}
	return out
}

// reversePairs 横向条形图需要"最大在最上", ECharts 类目轴自下而上 → 反转。
func reversePairs(p [][2]any) [][2]any {
	for i, j := 0, len(p)-1; i < j; i, j = i+1, j-1 {
		p[i], p[j] = p[j], p[i]
	}
	return p
}

func (a *API) handleEcharts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(echartsJS)
}

func (a *API) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if a.adminToken == "" || r.URL.Query().Get("token") != a.adminToken {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"App": appNameCN, "Version": a.version}

	// ---- 用户 + 画像 ----
	prefRows, _ := a.st.AdminUserPrefRows()
	userRows := make([]userTblRow, 0, len(prefRows))
	for _, pr := range prefRows {
		tops := dimTops(pr.Profile, pr.Disliked)
		disN := 0
		if pr.Disliked != "" {
			var arr []string
			if err := json.Unmarshal([]byte(pr.Disliked), &arr); err == nil {
				disN = len(arr)
			}
		}
		userRows = append(userRows, userTblRow{
			UserID: pr.UserID, CreatedAt: pr.CreatedAt, LastAt: pr.LastPromptAt,
			PromptCount: pr.PromptCount, PackCN: packCN(pr.Packs),
			Style: tops["style"], Subject: tops["subject"], Palette: tops["palette"],
			Mood: tops["mood"], Comp: tops["composition"],
			Ver:      pr.ClientVer,
			DislikeN: disN, Disabled: pr.Disabled,
		})
	}
	data["UserRows"] = userRows

	// ---- KPI ----
	usersN := int64(len(prefRows))
	var kNew7 int64
	day7 := time.Now().AddDate(0, 0, -6).Format("2006-01-02")
	growth, _ := a.st.UserGrowthDaily(90)
	gRows := make([][]any, 0, len(growth))
	for _, g := range growth {
		gRows = append(gRows, []any{g.Day, g.New})
		if g.Day >= day7 {
			kNew7 += g.New
		}
	}
	data["KUsers"], data["KNew7"] = usersN, kNew7

	var kTodayGen, kTodayOK int64
	today := time.Now().Format("2006-01-02")
	genTotal, genOK, genDaily, _ := a.st.GenStats(30)
	gdRows := make([][]any, 0, len(genDaily))
	for i := len(genDaily) - 1; i >= 0; i-- { // 倒序转升序
		d := genDaily[i]
		gdRows = append(gdRows, []any{d.Day, d.Total, d.Success})
		if d.Day == today {
			kTodayGen, kTodayOK = d.Total, d.Success
		}
	}
	rate := "—"
	if genTotal > 0 {
		rate = fmt.Sprintf("%.1f%%", float64(genOK)*100/float64(genTotal))
	}
	data["KTodayGen"], data["KTodayOK"], data["KRate"] = kTodayGen, kTodayOK, rate

	pv, uv, dl, imgDL, siteDaily, _ := a.st.SiteStats(30)
	data["KPV"], data["KUV"], data["KDL"], data["KImgDL"] = pv, uv, dl, imgDL
	apkDL, _ := a.st.SiteEventCount("apk_dl")
	data["KApkDL"] = apkDL
	stRows := make([][]any, 0, len(siteDaily))
	for i := len(siteDaily) - 1; i >= 0; i-- {
		s := siteDaily[i]
		stRows = append(stRows, []any{s.Day, s.PV, s.UV, s.DL, s.ImgDL})
	}

	// ---- 出图按来源 / 分辨率 / combo ----
	srcRows, _ := a.st.GenDailyBySource(30)
	daysSet := map[string]bool{}
	srcSeen := map[string]map[string]int64{}
	for _, r := range srcRows {
		daysSet[r.Day] = true
		if srcSeen[r.Source] == nil {
			srcSeen[r.Source] = map[string]int64{}
		}
		srcSeen[r.Source][r.Day] = r.Total
	}
	var dayList []string
	for d := range daysSet {
		dayList = append(dayList, d)
	}
	sort.Strings(dayList)
	srcSeries := map[string][]int64{}
	srcNames := make([]string, 0, len(srcSeen))
	for n := range srcSeen {
		srcNames = append(srcNames, n)
	}
	sort.Strings(srcNames)
	for _, n := range srcNames {
		series := make([]int64, 0, len(dayList))
		for _, d := range dayList {
			series = append(series, srcSeen[n][d])
		}
		srcSeries[n] = series
	}

	resRows, _ := a.st.ResolutionTop(30, 10)
	resPairs := make([][2]any, 0, len(resRows))
	for _, r := range resRows {
		resPairs = append(resPairs, [2]any{fmt.Sprintf("%dx%d", r.W, r.H), r.N})
	}

	comboRows, _ := a.st.ComboFreq(30)
	useSubj, useStyle := buildComboUse(comboRows)

	// ---- 偏好聚合 ----
	packs, likes, dislikes := buildPrefAgg(prefRows)
	likeOut := map[string][][2]any{}
	for _, d := range taxonomy.Dimensions {
		likeOut[d.ID] = reversePairs(topN(likes[d.ID], 10))
	}

	// ---- 用量 ----
	usageRows, _ := a.st.UsageDaily(7)
	uDays := make([]string, 0, len(usageRows))
	uLLM := make([]int64, 0, len(usageRows))
	uGen := make([]int64, 0, len(usageRows))
	for _, u := range usageRows {
		uDays = append(uDays, u.Day)
		uLLM = append(uLLM, u.LLM)
		uGen = append(uGen, u.Gen)
	}

	// ---- 系统 ----
	schema, _ := a.st.CurrentSchema()
	pgTotal, pgReady, _ := a.st.PregenStats()
	data["KSchema"] = schema
	data["KPregen"] = fmt.Sprintf("%d / %d", pgReady, pgTotal)
	data["KQuotaG"] = fmt.Sprintf("%d / %d", a.genHour, a.genDay)
	data["KQuotaL"] = fmt.Sprintf("%d / %d", a.llmHour, a.llmDay)

	verRows, _ := a.st.ClientVerDist()
	verMap := map[string]int64{}
	for _, v := range verRows {
		verMap[v.Ver] = v.N
	}

	// ---- 表格 ----
	type pr struct {
		At, User, Source, WorkflowID, Positive, Negative string
		W, H                                             int
		Success                                          bool
	}
	var prompts []pr
	if rows, err := a.st.AdminRecentPrompts(20); err == nil {
		for _, x := range rows {
			prompts = append(prompts, pr{x.At, x.UserID, x.Source, x.WorkflowID, x.Positive, x.Negative, x.Width, x.Height, x.Success})
		}
	}
	data["Prompts"] = prompts

	type sg struct{ At, User, Type, Detail string }
	var sigs []sg
	if rows, err := a.st.AdminRecentSignals(30); err == nil {
		for _, x := range rows {
			sigs = append(sigs, sg{x.At, x.UserID, x.Type, x.Detail})
		}
	}
	data["Signals"] = sigs

	if jobs, err := a.st.AdminRecentJobs(15); err == nil {
		data["Jobs"] = jobs
	} else {
		data["Jobs"] = []store.AdminJobRow{}
	}
	if devs, err := a.st.AdminDeviceRows(50); err == nil {
		data["Devices"] = devs
	} else {
		data["Devices"] = []store.AdminDeviceRow{}
	}
	if evs, err := a.st.AdminRecentSiteEvents(20); err == nil {
		data["SiteEvents"] = evs
	} else {
		data["SiteEvents"] = []store.SiteEventRow{}
	}

	// ---- 图表数据(JSON 内嵌) ----
	chart := map[string]any{
		"gen_daily":  gdRows,
		"growth":     gRows,
		"site_daily": stRows,
		"src":        map[string]any{"days": dayList, "series": srcSeries},
		"res":        reversePairs(resPairs),
		"packs":      reversePairs(topN(packs, 12)),
		"likes":      likeOut,
		"dislikes":   reversePairs(topN(dislikes, 10)),
		"usage":      map[string]any{"days": uDays, "llm": uLLM, "gen": uGen},
		"clientver":  reversePairs(topN64(verMap, 20)),
		"use":        map[string]any{"subject": reversePairs(topN64(useSubj, 10)), "style": reversePairs(topN64(useStyle, 10))},
	}
	b, err := json.Marshal(chart)
	if err != nil {
		b = []byte("{}")
	}
	data["DataJS"] = template.JS(b)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminTmpl.Execute(w, data)
}
