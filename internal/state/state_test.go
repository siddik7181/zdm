package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPhasesAreOrdered(t *testing.T) {
	s := New("h")
	if s.Next() != Preflight {
		t.Fatalf("next = %s", s.Next())
	}
	if err := s.Complete(Verify, time.Now()); err == nil {
		t.Fatal("skipping phases must fail")
	}
	for _, p := range []Phase{Preflight, Migrate, Verify} {
		if err := s.Complete(p, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if s.Next() != Done || s.Complete(Verify, time.Now()) == nil {
		t.Fatal("finished state must be Done and not repeatable")
	}
}

func TestSaveLoadAndPlanMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	s := New("h1")
	if err := s.Complete(Preflight, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, "h1")
	if err != nil || !got.Has(Preflight) || got.Next() != Migrate {
		t.Fatalf("load: %+v %v", got, err)
	}
	if _, err := Load(path, "other"); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("want ErrPlanChanged, got %v", err)
	}
	if fresh, err := Load(filepath.Join(t.TempDir(), "none"), "h"); err != nil || fresh.Next() != Preflight {
		t.Fatalf("missing file: %+v %v", fresh, err)
	}
}
