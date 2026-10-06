// Package almanac 节日/节气语境引擎: 为提示词规划提供"语境轻推"素材。
// 设计原则(见产品红线): 语境仅作画面氛围的含蓄呼应, 不覆盖用户画像;
// 永不生成文字/横幅/祝福语; 节气优先于公历节日; 敏感节令(清明等)庄重含蓄(提醒非庆祝)。
package almanac

import (
	"fmt"
	"sort"
	"time"
)

// Kind 语境类别。
type Kind string

const (
	KindTerm     Kind = "solar_term" // 二十四节气
	KindFestival Kind = "festival"   // 公历节日(含动态日期)
	KindLunar    Kind = "lunar"      // 农历节日(按年查表)
)

// Event 一个语境事件。
type Event struct {
	Date      time.Time // 公历日期(当日 0 点)
	Name      string    // 名称, 如 "立春"、"母亲节"
	Scene     string    // 画面语境描述(中文, 供 LLM 规划画面氛围)
	Kind      Kind
	Sensitive bool // 敏感节令: 提醒非庆祝(清明等)
}

// windowDays 默认前瞻窗口(天)。
const windowDays = 3

// solarTerms 二十四节气(近似公历日期 MM-DD; 每年浮动 ±1 天, 窗口期内即覆盖)。
var solarTerms = []struct{ md, name, scene string }{
	{"01-05", "小寒", "深冬清寒, 白雪与苍松的素净画面"},
	{"01-20", "大寒", "岁末极寒, 冰凌与远山的静谧冷调"},
	{"02-04", "立春", "初春解冻, 嫩芽破土, 淡青鹅黄的清新气息"},
	{"02-19", "雨水", "春雨如丝, 湿润清新的青灰色调"},
	{"03-05", "惊蛰", "春雷初动, 万物复苏的鲜活绿意"},
	{"03-20", "春分", "昼夜均分, 繁花与新绿的明媚平衡感"},
	{"04-05", "清明", "清明细雨润物, 杏花青柳的淡雅画卷, 庄重含蓄的追思氛围"},
	{"04-20", "谷雨", "暮春细雨, 新茶与青山的湿润清新"},
	{"05-05", "立夏", "初夏明媚, 浓绿树影与清澈光线的活力感"},
	{"05-21", "小满", "麦浪初黄, 田野与暖风的丰盈充实"},
	{"06-06", "芒种", "仲夏忙种, 金色麦田与晴空的热烈"},
	{"06-21", "夏至", "盛夏蝉鸣, 浓荫与明亮光斑的慵懒惬意"},
	{"07-07", "小暑", "暑气初盛, 荷塘与扇影的清凉对照"},
	{"07-23", "大暑", "盛夏炎阳, 碧水蓝天与淋漓水色的清凉诉求"},
	{"08-07", "立秋", "秋意初现, 微凉晚风, 金绿交织的旷野"},
	{"08-23", "处暑", "暑气渐消, 澄澈天空与初黄的安宁"},
	{"09-07", "白露", "白露凝霜, 芦苇与晨雾的朦胧微凉"},
	{"09-23", "秋分", "秋高气爽, 金黄原野的均衡丰盈"},
	{"10-08", "寒露", "深秋寒露, 枫红与冷雾的浓郁清冷"},
	{"10-23", "霜降", "霜染层林, 橙红与苍蓝的深秋韵味"},
	{"11-07", "立冬", "初冬静美, 薄霜与暖灯的冷暖交汇"},
	{"11-22", "小雪", "初雪轻落, 灰白与暖木色的静谧"},
	{"12-07", "大雪", "大雪纷飞, 银装素裹的壮美静谧"},
	{"12-21", "冬至", "冬至长夜, 暖灯映雪的温馨, 或清冽星空的静夜"},
}

// fixedFestivals 公历固定节日(克制选取, 只保留语义适合画面氛围的)。
var fixedFestivals = []struct{ md, name, scene string }{
	{"01-01", "元旦", "新年伊始的晨光与希冀, 崭新启程的轻快氛围"},
	{"02-14", "情人节", "温柔玫瑰色光影, 含蓄浪漫的暖调氛围"},
	{"03-08", "妇女节", "春日柔光与花卉, 温婉优雅的致敬氛围"},
	{"05-01", "劳动节", "晴朗假日的松弛感, 远行或田园的惬意"},
	{"06-01", "儿童节", "明亮跳跃的色彩与童趣元素, 纯真活泼"},
	{"10-01", "国庆节", "金秋十月的壮阔山河, 沉静厚重的自豪感"},
}

// dynamicFestivals 按年计算日期的节日。
var dynamicFestivals = []struct {
	name  string
	date  func(year int) time.Time
	scene string
}{
	{"母亲节", func(y int) time.Time { return nthWeekday(y, time.May, time.Sunday, 2) },
		"康乃馨般的柔粉暖光, 温婉含蓄的感恩氛围"},
	{"父亲节", func(y int) time.Time { return nthWeekday(y, time.June, time.Sunday, 3) },
		"沉稳暖棕色调, 如山般厚重可靠的安宁感"},
}

// lunarEntry 农历节日(公历日期按年查证, 增补新年度时须核对权威农历数据)。
type lunarEntry struct {
	date  time.Time
	name  string
	scene string
	sens  bool
}

// lunarFestivals 农历节日公历日期表(2026 年已核对多个来源; 跨年补表)。
var lunarFestivals = map[int][]lunarEntry{
	2026: {
		{time.Date(2026, 2, 17, 0, 0, 0, 0, time.Local), "春节", "新春团圆, 灯笼暖光与红金交织的年味氛围", false},
		{time.Date(2026, 3, 3, 0, 0, 0, 0, time.Local), "元宵节", "元宵灯会, 暖黄灯火与圆月的团圆氛围", false},
		{time.Date(2026, 6, 19, 0, 0, 0, 0, time.Local), "端午节", "端午龙舟, 碧水青艾与粽叶的清爽气息", false},
		{time.Date(2026, 8, 19, 0, 0, 0, 0, time.Local), "七夕", "七夕星河, 鹊桥与银河的浪漫古典意境", false},
		{time.Date(2026, 9, 25, 0, 0, 0, 0, time.Local), "中秋节", "中秋圆月, 桂影与暖灯的团圆静美", false},
		{time.Date(2026, 10, 18, 0, 0, 0, 0, time.Local), "重阳节", "重阳登高, 金菊与远山的清朗旷达", false},
	},
}

// Within 返回 [now, now+days] 内的语境事件(含今天, 按日期升序); days<=0 时用默认 3 天。
// 同时检查今年与明年条目, 覆盖岁末跨年窗口。
func Within(now time.Time, days int) []Event {
	if days <= 0 {
		days = windowDays
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, days)
	var out []Event

	add := func(d time.Time, name, scene string, k Kind, sens bool) {
		dd := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
		if dd.Before(start) || dd.After(end) {
			return
		}
		out = append(out, Event{Date: dd, Name: name, Scene: scene, Kind: k, Sensitive: sens})
	}

	for _, y := range []int{now.Year(), now.Year() + 1} {
		for _, st := range solarTerms {
			add(mdDate(y, st.md), st.name, st.scene, KindTerm, st.name == "清明")
		}
		for _, f := range fixedFestivals {
			add(mdDate(y, f.md), f.name, f.scene, KindFestival, false)
		}
		for _, f := range dynamicFestivals {
			add(f.date(y), f.name, f.scene, KindFestival, false)
		}
		for _, lf := range lunarFestivals[y] {
			add(lf.date, lf.name, lf.scene, KindLunar, lf.sens)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// mdDate 将 "MM-DD" 解析为指定年份的日期。
func mdDate(year int, md string) time.Time {
	var m, d int
	_, _ = fmt.Sscanf(md, "%02d-%02d", &m, &d)
	return time.Date(year, time.Month(m), d, 0, 0, 0, 0, time.Local)
}

// nthWeekday 返回某年某月第 n 个星期 w 的日期(n 从 1 起)。
func nthWeekday(year int, month time.Month, w time.Weekday, n int) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.Local)
	offset := (int(w) - int(first.Weekday()) + 7) % 7
	return first.AddDate(0, 0, offset+(n-1)*7)
}
