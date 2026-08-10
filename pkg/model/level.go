package model

import "math"

// ExpPerLevel is the base exp cost of reaching level 1.
const ExpPerLevel = 100

// LevelGrowth is the multiplicative step between successive level costs.
// Level N costs ExpPerLevel * LevelGrowth^(N-1).
const LevelGrowth = 1.05

// MaxLevel is the cap on the derived level; the formula grows fast enough
// that hitting MaxLevel with the daily 100 grant is effectively impossible.
const MaxLevel = 200

// LevelForExp returns the integer level corresponding to a given amount of
// lifetime experience points.
//
// Formula: the threshold for reaching level N is
//
//	sum_{k=1}^{N-1} 100 * 1.05^k  =  100 * (1.05^N - 1.05) / 0.05
//
// We invert it as level = ceil(log_{1.05}(1 + 0.05*exp/100)) and clamp to
// [0, MaxLevel]. A user starts at level 0 (no exp).
func LevelForExp(exp int64) int {
	if exp <= 0 {
		return 0
	}
	raw := math.Ceil(math.Log(1+LevelGrowth*float64(exp)/float64(ExpPerLevel)) / math.Log(LevelGrowth))
	level := int(raw)
	if level < 0 {
		return 0
	}
	if level > MaxLevel {
		return MaxLevel
	}
	return level
}

// ExpForLevel returns the cumulative exp required to *reach* [level].
func ExpForLevel(level int) int64 {
	if level <= 0 {
		return 0
	}
	// Closed form for the geometric sum.
	cost := float64(ExpPerLevel) * (math.Pow(LevelGrowth, float64(level)) - LevelGrowth) / (LevelGrowth - 1)
	return int64(math.Ceil(cost))
}

// ExpProgress returns the fraction (in [0,1]) of progress through the current
// level, plus the exp required for the next level. The current level is
// derived from [exp]; the next level threshold is what [exp] must reach to
// advance.
func ExpProgress(exp int64) (fraction float64, nextLevelThreshold int64, nextLevel int) {
	level := LevelForExp(exp)
	if level >= MaxLevel {
		return 1, exp, MaxLevel
	}
	currentThreshold := ExpForLevel(level)
	nextThreshold := ExpForLevel(level + 1)
	span := nextThreshold - currentThreshold
	if span <= 0 {
		return 1, nextThreshold, level + 1
	}
	into := exp - currentThreshold
	if into < 0 {
		into = 0
	}
	if into > span {
		into = span
	}
	return float64(into) / float64(span), nextThreshold, level + 1
}
