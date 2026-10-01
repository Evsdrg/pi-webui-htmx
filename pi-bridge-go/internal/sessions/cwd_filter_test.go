package sessions

import (
	"context"
	"testing"
)

// 按工作区筛选会话列表：多工作区场景下要能只看其中一个。
//
// 这条需求的来源是真实使用：侧栏把所有项目的会话混在一起，找一个
// 「刚才那个项目的对话」只能靠翻。工作区是会话自带的属性（Pi 写入的 cwd），
// 不需要用户手工打标签。
func Test按工作区筛选会话(t *testing.T) {
	ctx := context.Background()
	cwdA := t.TempDir()
	cwdB := t.TempDir()
	store, dir := newStore(t, cwdA, cwdB)
	writeSession(t, dir, "a1", cwdA, entry("u1", ""))
	writeSession(t, dir, "b1", cwdB, entry("u2", ""))
	writeSession(t, dir, "a2", cwdA, entry("u3", ""))

	all, err := store.List(ctx, 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 3 {
		t.Fatalf("不筛选应返回全部 3 条，实际 %d", len(all.Items))
	}

	onlyA, err := store.List(ctx, 0, 10, cwdA)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyA.Items) != 2 {
		t.Fatalf("工作区 A 应有 2 条，实际 %d", len(onlyA.Items))
	}
	for _, h := range onlyA.Items {
		if h.Cwd != cwdA {
			t.Fatalf("筛选结果混入了别的工作区: %s", h.Cwd)
		}
	}
	if onlyA.Cwd != cwdA {
		t.Fatalf("Listing.Cwd 应回显当前筛选，实际 %q", onlyA.Cwd)
	}

	// 工作区清单要给出全部候选与计数，而不是只有当前页里的。
	if len(onlyA.Cwds) != 2 {
		t.Fatalf("应列出 2 个工作区，实际 %d: %+v", len(onlyA.Cwds), onlyA.Cwds)
	}
	counts := map[string]int{}
	for _, c := range onlyA.Cwds {
		counts[c.Cwd] = c.Count
	}
	if counts[cwdA] != 2 || counts[cwdB] != 1 {
		t.Fatalf("工作区计数不对: %+v", counts)
	}
}

// 筛选必须发生在分页之前。
//
// 反例：先按 offset 在全集上切片、再过滤，第二页就会漏条目——
// 「加载更多」看起来少了东西，而且不会报任何错。
func Test筛选在分页之前(t *testing.T) {
	ctx := context.Background()
	cwdA := t.TempDir()
	cwdB := t.TempDir()
	store, dir := newStore(t, cwdA, cwdB)
	// 交错写入：两个工作区各有 3 条。
	for _, id := range []string{"a1", "b1", "a2", "b2", "a3", "b3"} {
		cwd := cwdA
		if id[0] == 'b' {
			cwd = cwdB
		}
		writeSession(t, dir, id, cwd, entry("u"+id, ""))
	}

	seen := map[string]bool{}
	offset := 0
	for {
		page, err := store.List(ctx, offset, 2, cwdA)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range page.Items {
			if h.Cwd != cwdA {
				t.Fatalf("分页结果混入别的工作区: %s", h.Cwd)
			}
			if seen[h.ID] {
				t.Fatalf("条目重复出现: %s", h.ID)
			}
			seen[h.ID] = true
		}
		if !page.HasMore {
			break
		}
		offset += len(page.Items)
	}
	if len(seen) != 3 {
		t.Fatalf("翻完应有 3 条，实际 %d: %v", len(seen), seen)
	}
	// HasMore 必须相对筛选后的集合：3 条 / 每页 2 条，第二页后应停止。
	if _, err := store.List(ctx, 2, 2, cwdA); err != nil {
		t.Fatal(err)
	}
	last, err := store.List(ctx, 2, 2, cwdA)
	if err != nil {
		t.Fatal(err)
	}
	if last.HasMore {
		t.Fatal("筛选集合只有 3 条，取到第 3 条后不应再报 HasMore")
	}
}

// 搜索要跟随工作区筛选。
//
// 用户先筛到某个工作区、再在搜索框输入时，期待的是「在眼前这批里搜」；
// 返回别的项目的命中会很难理解。
func Test搜索跟随工作区筛选(t *testing.T) {
	ctx := context.Background()
	cwdA := t.TempDir()
	cwdB := t.TempDir()
	store, dir := newStore(t, cwdA, cwdB)
	writeSession(t, dir, "a1", cwdA, entryRole("u1", "", "user"))
	writeSession(t, dir, "b1", cwdB, entryRole("u2", "", "user"))

	limits := DefaultSearchLimits()
	all, err := store.Search(ctx, "u", limits)
	if err != nil {
		t.Fatal(err)
	}
	base := len(all.Matches)
	if base == 0 {
		t.Fatal("不筛选时应搜到内容")
	}

	limits.Cwd = cwdA
	onlyA, err := store.Search(ctx, "u", limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyA.Matches) == 0 {
		t.Fatal("工作区 A 内应搜到内容")
	}
	if len(onlyA.Matches) >= base && base > 1 {
		t.Fatalf("筛选后命中数应减少：全部 %d，工作区 A %d", base, len(onlyA.Matches))
	}
	for _, m := range onlyA.Matches {
		if m.Cwd != cwdA {
			t.Fatalf("搜索命中混入别的工作区: %s", m.Cwd)
		}
	}
}

// cwd 归一化：手改过的配置可能带尾斜杠，不该因此筛不到。
func Test工作区筛选容忍尾斜杠(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	store, dir := newStore(t, cwd)
	writeSession(t, dir, "s1", cwd, entry("u1", ""))

	list, err := store.List(ctx, 0, 10, cwd+"/")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("带尾斜杠的筛选应仍能命中，实际 %d 条", len(list.Items))
	}
	if list.Cwd != cwd {
		t.Fatalf("回显的 cwd 应为归一化后的值，实际 %q", list.Cwd)
	}
}
