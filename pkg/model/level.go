package model

import "math"

// ExpPerLevel is the exp a user must accumulate to advance from level 1 to
// level 2.
const ExpPerLevel = 100

// LevelGrowth is the multiplicative step between successive level costs: each
// level costs 5% more than the one before (rounded to the nearest integer).
const LevelGrowth = 1.05

// MaxLevel is the cap on the derived level.
const MaxLevel = 200

// levelCosts[i] is the exp required to advance from level i+1 to level i+2.
// Cost 1 = ExpPerLevel; every later cost is the previous cost times
// LevelGrowth, rounded to the nearest integer (matching the client).
var levelCosts = func() []int64 {
	costs := make([]int64, MaxLevel)
	cost := int64(ExpPerLevel)
	for i := 0; i < MaxLevel; i++ {
		costs[i] = cost
		cost = int64(math.Round(float64(cost) * LevelGrowth))
	}
	return costs
}()

// levelThresholds[i] is the cumulative exp required to *reach* level i+1
// (the sum of levelCosts[0..i-1]). levelThresholds[0] = 0 because level 1 is
// the starting level and needs no exp.
var levelThresholds = func() []int64 {
	thresholds := make([]int64, MaxLevel+1)
	for i := 0; i < MaxLevel; i++ {
		thresholds[i+1] = thresholds[i] + levelCosts[i]
	}
	return thresholds
}()

// LevelCost returns the exp required to advance from [level] to level+1.
// Returns 0 outside [1, MaxLevel].
func LevelCost(level int) int64 {
	if level < 1 || level > MaxLevel {
		return 0
	}
	return levelCosts[level-1]
}

// ExpForLevel returns the cumulative exp required to *reach* [level]. Level 1
// requires 0 (every account starts there).
func ExpForLevel(level int) int64 {
	if level <= 1 {
		return 0
	}
	if level > MaxLevel {
		return levelThresholds[MaxLevel]
	}
	return levelThresholds[level-1]
}

// LevelForExp returns the level corresponding to a given amount of lifetime
// total experience: the highest level whose cumulative threshold does not
// exceed exp. Always at least 1, never more than MaxLevel.
func LevelForExp(exp int64) int {
	if exp <= 0 {
		return 1
	}
	lo, hi := 1, MaxLevel
	for lo <= hi {
		mid := (lo + hi) / 2
		if levelThresholds[mid-1] <= exp {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return hi
}

// ExpProgress returns the fraction (in [0,1]) of progress through the current
// level, plus the exp required to reach the next level. At MaxLevel the
// fraction is pinned to 1.
func ExpProgress(exp int64) (fraction float64, nextLevelThreshold int64, nextLevel int) {
	level := LevelForExp(exp)
	if level >= MaxLevel {
		return 1, ExpForLevel(MaxLevel), MaxLevel
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
