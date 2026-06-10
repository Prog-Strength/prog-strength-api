package exercise

import (
	"reflect"
	"testing"
)

func TestMovementPattern_MuscleGroups(t *testing.T) {
	cases := []struct {
		pattern MovementPattern
		want    []MuscleGroup
	}{
		{MovementPush, []MuscleGroup{MuscleChest, MuscleShoulders, MuscleTriceps}},
		{MovementPull, []MuscleGroup{MuscleBack, MuscleBiceps, MuscleForearms}},
		{MovementLegs, []MuscleGroup{MuscleQuads, MuscleHamstrings, MuscleGlutes, MuscleCalves}},
		{MovementCore, []MuscleGroup{MuscleCore}},
		{MovementAll, AllMuscleGroups()},
	}
	for _, c := range cases {
		t.Run(string(c.pattern), func(t *testing.T) {
			got := c.pattern.MuscleGroups()
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s.MuscleGroups() = %v, want %v", c.pattern, got, c.want)
			}
		})
	}
}

func TestMovementAll_IncludesEveryCatalogMuscle(t *testing.T) {
	all := MovementAll.MuscleGroups()
	allCatalog := []MuscleGroup{
		MuscleChest, MuscleBack, MuscleShoulders, MuscleBiceps, MuscleTriceps,
		MuscleForearms, MuscleCore, MuscleQuads, MuscleHamstrings, MuscleGlutes,
		MuscleCalves,
	}
	if len(all) != len(allCatalog) {
		t.Fatalf("MovementAll resolves to %d groups, want %d", len(all), len(allCatalog))
	}
	set := make(map[MuscleGroup]bool, len(all))
	for _, mg := range all {
		set[mg] = true
	}
	for _, mg := range allCatalog {
		if !set[mg] {
			t.Errorf("MovementAll missing catalog muscle %q", mg)
		}
		if !mg.Valid() {
			t.Errorf("catalog muscle %q is not Valid()", mg)
		}
	}
}

func TestMovementPattern_Valid(t *testing.T) {
	cases := []struct {
		pattern MovementPattern
		want    bool
	}{
		{MovementPush, true},
		{MovementPull, true},
		{MovementLegs, true},
		{MovementCore, true},
		{MovementAll, true},
		{MovementPattern(""), false},
		{MovementPattern("upper"), false},
		{MovementPattern("PUSH"), false},
		{MovementPattern("chest"), false},
	}
	for _, c := range cases {
		t.Run(string(c.pattern), func(t *testing.T) {
			if got := c.pattern.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.pattern, got, c.want)
			}
		})
	}
}

func TestMovementPattern_MuscleGroupsUnknownIsNil(t *testing.T) {
	if got := MovementPattern("unknown").MuscleGroups(); got != nil {
		t.Errorf("unknown pattern should resolve to nil, got %v", got)
	}
}
