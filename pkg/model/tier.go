package model

// TierLabel returns the localized-friendly tier label and hex color for a
// given level. Tiers change every 10 levels.
func TierLabel(level int) (name string, color string) {
	tier := level / 10
	switch tier {
	case 0:
		return "Newcomer", "#9E9E9E"
	case 1:
		return "Apprentice", "#7CB342"
	case 2:
		return "Initiate", "#43A047"
	case 3:
		return "Adept", "#00897B"
	case 4:
		return "Journeyman", "#039BE5"
	case 5:
		return "Expert", "#1E88E5"
	case 6:
		return "Veteran", "#3949AB"
	case 7:
		return "Champion", "#5E35B1"
	case 8:
		return "Hero", "#8E24AA"
	case 9:
		return "Legend", "#D81B60"
	case 10:
		return "Mythic", "#E53935"
	case 11:
		return "Ascendant", "#F4511E"
	case 12:
		return "Eternal", "#FF6F00"
	case 13:
		return "Transcendent", "#FFB300"
	default:
		return "Supreme", "#FBC02D"
	}
}
