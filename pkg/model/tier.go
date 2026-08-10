package model

// TierLabel returns the tier name and hex color for a given level. Tiers
// change every 10 levels (1-10, 11-20, ... 191-200). The colour also drives
// the experience bar in the clients.
func TierLabel(level int) (name string, color string) {
	switch {
	case level <= 10:
		return "出发", "#9E9E9E"
	case level <= 20:
		return "徒步", "#7CB342"
	case level <= 30:
		return "听风", "#42A5F5"
	case level <= 40:
		return "赤足", "#B7611A"
	case level <= 50:
		return "燃火", "#E64A19"
	case level <= 60:
		return "共行", "#FFB300"
	case level <= 70:
		return "迷途", "#9575CD"
	case level <= 80:
		return "自鸣", "#EC6B8F"
	case level <= 90:
		return "越岭", "#757575"
	case level <= 100:
		return "高原", "#2C3E70"
	case level <= 110:
		return "观星", "#5B2C8E"
	case level <= 120:
		return "入画", "#A1672C"
	case level <= 130:
		return "风蚀", "#D4B86A"
	case level <= 140:
		return "绿洲", "#2E8B57"
	case level <= 150:
		return "如石", "#37474F"
	case level <= 160:
		return "俯瞰", "#4A90D9"
	case level <= 170:
		return "合一", "#00C48C"
	case level <= 180:
		return "回响", "#D9A13E"
	case level <= 190:
		return "无名", "#1F1F1F"
	default:
		return "源起", "#4A90D9"
	}
}
