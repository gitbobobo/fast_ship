package handler

import (
	"strconv"
	"testing"
)

func TestQueuedEditTextRejectsOmittedField(t *testing.T) {
	if _, ok := queuedEditText(nil); ok {
		t.Fatal("omitted text must be rejected")
	}
	empty := ""
	got, ok := queuedEditText(&empty)
	if !ok || got != "" {
		t.Fatalf("explicit empty string must pass, got (%q, %v)", got, ok)
	}
	body := "改文案"
	got, ok = queuedEditText(&body)
	if !ok || got != body {
		t.Fatalf("present text must pass, got (%q, %v)", got, ok)
	}
}

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
