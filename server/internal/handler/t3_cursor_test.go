package handler

import (
	"strconv"
	"testing"
)

func TestParseOptionalCursorQuery(t *testing.T) {
	got, err := parseOptionalCursorQuery("")
	if err != nil || got != nil {
		t.Fatalf("empty = (%v, %v)", got, err)
	}
	got, err = parseOptionalCursorQuery("0")
	if err != nil || got == nil || *got != 0 {
		t.Fatalf("zero = (%v, %v)", got, err)
	}
	got, err = parseOptionalCursorQuery("12")
	if err != nil || got == nil || *got != 12 {
		t.Fatalf("12 = (%v, %v)", got, err)
	}
	if _, err = parseOptionalCursorQuery("nope"); err == nil {
		t.Fatal("expected error for non-integer")
	}
	if _, err = parseOptionalCursorQuery("-1"); err != strconv.ErrRange {
		t.Fatalf("expected ErrRange for -1, got %v", err)
	}
}
