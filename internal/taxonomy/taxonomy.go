// Package taxonomy 定义 5 维偏好属性词典。
// 每个维度的每个值附英文提示词片段(用于 SDXL / Z-Image)与可选负面片段;
// 探针图组合、画像权重、提示词拼装均以本词典为准。
package taxonomy

// Value 一个属性值。
type Value struct {
	ID       string // 英文键(画像与组合引用)
	NameCN   string // 中文名(界面展示)
	Prompt   string // 正向提示词片段
	Negative string // 负面片段(该值"不喜欢"时合入负面提示词, 可为空)
}

// Dimension 一个偏好维度。
type Dimension struct {
	ID     string // style | subject | palette | mood | composition
	NameCN string
	Values []Value
}

// DimStyle / DimSubject / ... 维度 ID 常量。
const (
	DimStyle       = "style"
	DimSubject     = "subject"
	DimPalette     = "palette"
	DimMood        = "mood"
	DimComposition = "composition"
)

// BaseNegative 通用负面提示词。
const BaseNegative = "lowres, blurry, jpeg artifacts, watermark, text, signature, " +
	"deformed, disfigured, out of frame, worst quality, low quality"

// QualitySuffix 正向提示词质量后缀。
const QualitySuffix = "high detail, sharp focus, professional composition, 4k wallpaper"

// Dimensions 5 维词典(顺序即提示词拼装顺序)。
var Dimensions = []Dimension{
	{
		ID: DimStyle, NameCN: "风格",
		Values: []Value{
			{ID: "realism", NameCN: "写实", Prompt: "photorealistic, highly detailed photograph, natural lighting",
				Negative: "illustration, cartoon, painting"},
			{ID: "cyberpunk", NameCN: "赛博朋克", Prompt: "cyberpunk style, neon-lit futuristic cityscape, sci-fi atmosphere"},
			{ID: "ink", NameCN: "水墨国风", Prompt: "traditional chinese ink wash painting, shuimo style, elegant brush strokes, xuan paper texture"},
			{ID: "watercolor", NameCN: "水彩", Prompt: "delicate watercolor painting, soft washes, pigment bleeding, textured paper"},
			{ID: "minimal", NameCN: "极简", Prompt: "minimalist design, clean composition, generous negative space, flat tones"},
			{ID: "oil", NameCN: "厚涂油画", Prompt: "impasto oil painting, thick textured brushstrokes, expressive palette knife work"},
			{ID: "anime", NameCN: "二次元", Prompt: "anime style, japanese animation art, vibrant cel shading, clean linework",
				Negative: "photorealistic, realistic photograph"},
			{ID: "anime3d", NameCN: "3D动漫", Prompt: "3d anime render, stylized 3d character, cinematic cg lighting"},
			{ID: "print", NameCN: "版画", Prompt: "woodblock print style, ukiyo-e inspired, bold graphic linework"},
			{ID: "chalk", NameCN: "粉笔画", Prompt: "chalk pastel drawing, soft chalk texture, hand-drawn strokes"},
			{ID: "film", NameCN: "复古胶片", Prompt: "retro film photography, 90s kodak film grain, nostalgic analog aesthetic"},
			{ID: "pixel", NameCN: "像素风", Prompt: "pixel art, retro game graphics, 16-bit style"},
			{ID: "pop", NameCN: "波普艺术", Prompt: "pop art style, bold flat colors, halftone dots, warhol inspired"},
			{ID: "impressionist", NameCN: "印象派", Prompt: "impressionist painting, soft luminous light, visible brushstrokes, monet style"},
			{ID: "steampunk", NameCN: "蒸汽朋克", Prompt: "steampunk style, brass gears, steam machinery, victorian sci-fi"},
			{ID: "lowpoly", NameCN: "低多边形", Prompt: "low-poly 3d render, faceted geometric style"},
			{ID: "clay", NameCN: "黏土定格", Prompt: "claymation stop-motion style, hand-crafted clay figures, playful"},
			{ID: "papercut", NameCN: "剪纸皮影", Prompt: "chinese paper-cut art, layered shadow puppet style"},
			{ID: "flat", NameCN: "扁平插画", Prompt: "flat illustration, children's picture book style, clean shapes"},
			{ID: "neochinese", NameCN: "新中式", Prompt: "modern chinese aesthetic, elegant contemporary oriental design"},
			{ID: "neonart", NameCN: "霓虹光绘", Prompt: "neon light painting, glowing light trails, luminous long-exposure photography"},
			{ID: "poster", NameCN: "电影海报", Prompt: "cinematic movie poster style, dramatic composition, epic title card feel"},
		},
	},
	{
		ID: DimSubject, NameCN: "题材",
		Values: []Value{
			{ID: "nature", NameCN: "自然风光", Prompt: "breathtaking natural landscape, mountains and valleys, scenic vista"},
			{ID: "citynight", NameCN: "城市夜景", Prompt: "city skyline at night, glowing windows, urban nightscape"},
			{ID: "space", NameCN: "星空宇宙", Prompt: "deep space, starfield, nebula clouds, distant galaxy, cosmic vista"},
			{ID: "abstract", NameCN: "抽象几何", Prompt: "abstract geometric forms, flowing shapes, modern art"},
			{ID: "plants", NameCN: "植物花卉", Prompt: "lush botanical close-up, blooming flowers, delicate foliage detail"},
			{ID: "animal", NameCN: "动物", Prompt: "wild animal in natural habitat, wildlife portrait, fur detail"},
			{ID: "male", NameCN: "男性人像", Prompt: "portrait of a stylish man, masculine fashion photography"},
			{ID: "female", NameCN: "女性人像", Prompt: "portrait of a stylish woman, elegant fashion photography"},
			{ID: "child", NameCN: "儿童", Prompt: "portrait of a cute child, innocent joyful scene"},
			{ID: "young", NameCN: "年青人", Prompt: "portrait of a young adult in their twenties, youthful energy"},
			{ID: "middle", NameCN: "中年人", Prompt: "portrait of a mature middle-aged person, dignified presence"},
			{ID: "elderly", NameCN: "老年人", Prompt: "portrait of an elderly person, warm wise expression"},
			{ID: "moto", NameCN: "摩托机车", Prompt: "motorcycle, powerful motorbike, dynamic automotive photography"},
			{ID: "racecar", NameCN: "赛车", Prompt: "racing car, motorsport scene, track day action"},
			{ID: "supercar", NameCN: "超跑", Prompt: "luxury supercar, sleek exotic sports car"},
			{ID: "weapon", NameCN: "武器枪械", Prompt: "displayed firearm, weapon showcase, detailed metal craftsmanship"},
			{ID: "jet", NameCN: "战斗机", Prompt: "fighter jet, military aircraft in flight, aviation photography"},
			{ID: "mecha", NameCN: "机甲", Prompt: "mecha robot, giant sci-fi robot, detailed mechanical armor"},
			{ID: "food", NameCN: "美食", Prompt: "delicious gourmet food, appetizing culinary photography"},
			{ID: "machine", NameCN: "精巧机械", Prompt: "intricate mechanical device, precision machinery, gears and cogs"},
			{ID: "building", NameCN: "建筑", Prompt: "architectural photography, impressive building, geometric facade"},
			{ID: "aurora", NameCN: "极光", Prompt: "aurora borealis, northern lights dancing over landscape"},
			{ID: "cloudsea", NameCN: "云海", Prompt: "sea of clouds above mountain peaks, dreamy aerial view"},
			{ID: "ocean", NameCN: "海洋", Prompt: "ocean seascape, tropical island, underwater world"},
			{ID: "pet", NameCN: "宠物", Prompt: "adorable pet cat or dog, heartwarming animal portrait"},
			{ID: "sport", NameCN: "运动", Prompt: "sports action scene, dynamic athlete in motion"},
			{ID: "mythic", NameCN: "神话生物", Prompt: "mythical creature, dragon or phoenix, epic fantasy creature"},
			{ID: "spaceship", NameCN: "太空飞船", Prompt: "spaceship, sci-fi starship, futuristic spacecraft"},
			{ID: "train", NameCN: "火车飞机", Prompt: "train or airplane, transportation photography, travel scene"},
			{ID: "oldstreet", NameCN: "老街巷", Prompt: "ancient alley, old street with lanterns, nostalgic townscape"},
			{ID: "seasons", NameCN: "四季", Prompt: "seasonal scenery, cherry blossom spring or autumn foliage or winter snow"},
			{ID: "fluid", NameCN: "流体艺术", Prompt: "fluid art, liquid abstract paint, swirling colors"},
			{ID: "music", NameCN: "乐器音乐", Prompt: "musical instrument close-up, live music stage, concert atmosphere"},
		},
	},
	{
		ID: DimPalette, NameCN: "色调",
		Values: []Value{
			{ID: "cool", NameCN: "冷蓝青", Prompt: "cool blue-teal color palette, cyan and deep blue tones"},
			{ID: "warm", NameCN: "暖阳橙", Prompt: "warm orange and golden amber palette, sunset glow tones"},
			{ID: "dark", NameCN: "暗黑氛围", Prompt: "dark moody palette, deep shadows, low-key lighting"},
			{ID: "morandi", NameCN: "低饱和莫兰迪", Prompt: "muted morandi color palette, desaturated soft tones, dusty pastel"},
			{ID: "neon", NameCN: "高饱和霓虹", Prompt: "vibrant neon saturated palette, electric magenta and cyan glow"},
			{ID: "pastel", NameCN: "柔和粉彩", Prompt: "soft pastel palette, gentle pink and cream tones, airy"},
		},
	},
	{
		ID: DimMood, NameCN: "情绪",
		Values: []Value{
			{ID: "serene", NameCN: "宁静", Prompt: "serene and tranquil atmosphere, calm and peaceful mood"},
			{ID: "majestic", NameCN: "壮阔", Prompt: "majestic and grand atmosphere, epic scale, awe-inspiring vista"},
			{ID: "cozy", NameCN: "温馨", Prompt: "cozy warm atmosphere, intimate and welcoming mood"},
			{ID: "mysterious", NameCN: "神秘", Prompt: "mysterious atmosphere, enigmatic and ethereal mood"},
			{ID: "vibrant", NameCN: "活力", Prompt: "vibrant energetic atmosphere, lively and dynamic mood"},
		},
	},
	{
		ID: DimComposition, NameCN: "构图",
		Values: []Value{
			{ID: "wide", NameCN: "广角全景", Prompt: "ultra-wide panoramic view, expansive horizon, deep depth of field"},
			{ID: "macro", NameCN: "微距特写", Prompt: "macro close-up shot, extreme fine detail"},
			{ID: "centered", NameCN: "居中式极简", Prompt: "centered minimalist composition, symmetrical balance"},
			{ID: "thirds", NameCN: "三分法", Prompt: "rule of thirds composition, balanced subject placement"},
			{ID: "silhouette", NameCN: "剪影", Prompt: "dramatic silhouette against glowing sky, backlit subject"},
		},
	},
}

// Get 按 ID 取维度; 不存在返回 nil。
func Get(dimID string) *Dimension {
	for i := range Dimensions {
		if Dimensions[i].ID == dimID {
			return &Dimensions[i]
		}
	}
	return nil
}

// ValueOf 按维度与值 ID 取值; 不存在返回 nil。
func ValueOf(dimID, valID string) *Value {
	d := Get(dimID)
	if d == nil {
		return nil
	}
	for i := range d.Values {
		if d.Values[i].ID == valID {
			return &d.Values[i]
		}
	}
	return nil
}

// NameCN 返回 "维度-值" 的中文描述(界面展示用)。
func NameCN(dimID, valID string) string {
	d := Get(dimID)
	if d == nil {
		return valID
	}
	for _, v := range d.Values {
		if v.ID == valID {
			return d.NameCN + "-" + v.NameCN
		}
	}
	return valID
}
