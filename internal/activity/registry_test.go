package activity

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestRegistry_LookupReturnsRegisteredDescriptor(t *testing.T) {
	running := &Descriptor{Type: ActivityRunning}
	walking := &Descriptor{Type: ActivityWalking}
	reg := NewRegistry(running, walking)

	got, err := reg.Lookup(ActivityRunning)
	if err != nil {
		t.Fatalf("Lookup(running): %v", err)
	}
	if got != running {
		t.Fatalf("Lookup(running) = %+v, want the registered running descriptor", got)
	}
}

func TestRegistry_LookupUnknownTypeReturnsErrUnknownActivityType(t *testing.T) {
	reg := NewRegistry(&Descriptor{Type: ActivityRunning})

	_, err := reg.Lookup(ActivityType("bogus"))
	if !errors.Is(err, ErrUnknownActivityType) {
		t.Fatalf("Lookup(bogus) err = %v, want ErrUnknownActivityType", err)
	}
	// The error names the offending value so a 422 body reads well.
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("Lookup(bogus) err = %q, want it to name the bad type", err)
	}
}

func TestRegistry_DuplicateRegistrationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewRegistry with a duplicate Type did not panic")
		}
	}()
	NewRegistry(
		&Descriptor{Type: ActivityRunning},
		&Descriptor{Type: ActivityRunning},
	)
}

func TestRegistry_TypesReturnsSortedTypeNames(t *testing.T) {
	// Registered deliberately out of lexical order.
	reg := NewRegistry(
		&Descriptor{Type: ActivityWalking},
		&Descriptor{Type: ActivityStrengthTraining},
		&Descriptor{Type: ActivityRunning},
		&Descriptor{Type: ActivityCycling},
	)

	got := reg.Types()
	want := []ActivityType{ActivityCycling, ActivityRunning, ActivityStrengthTraining, ActivityWalking}
	if len(got) != len(want) {
		t.Fatalf("Types() = %v, want %v", got, want)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i] < got[j] }) {
		t.Fatalf("Types() = %v, want sorted", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Types() = %v, want %v", got, want)
		}
	}
}
