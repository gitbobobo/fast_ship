package service

import "testing"

// 帧后回调只补登记「未由 armed 路径处理」的移除（#156 R7/R8）：已登记的
// 不再重复登记（否则会被归档流对账过的 ID 重新打回待对账）。
func TestT3IDsNotIn(t *testing.T) {
	got := t3IDsNotIn([]string{"a", "b", "c"}, []string{"b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("filter = %v, want [a c]", got)
	}
	if all := t3IDsNotIn([]string{"a"}, nil); len(all) != 1 || all[0] != "a" {
		t.Fatalf("no done set must keep all ids, got %v", all)
	}
	if none := t3IDsNotIn([]string{"a", "b"}, []string{"a", "b"}); len(none) != 0 {
		t.Fatalf("all done must filter everything, got %v", none)
	}
}
