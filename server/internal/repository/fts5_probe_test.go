package repository

// 临时探针：实测 glebarez/sqlite（modernc）驱动下 FTS5 + trigram 的可用性。
// 通过后并入正式测试文件；失败则按启动失败原则上报。

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestFTS5Probe(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if err := db.Exec(`CREATE VIRTUAL TABLE bot_memory_fts USING fts5(
		file_id UNINDEXED, bot_id UNINDEXED, path UNINDEXED, body,
		tokenize='trigram')`).Error; err != nil {
		t.Fatalf("create fts5 trigram table: %v", err)
	}

	ins := func(fileID, botID, path, body string) {
		if err := db.Exec(`INSERT INTO bot_memory_fts(file_id, bot_id, path, body) VALUES (?,?,?,?)`,
			fileID, botID, path, body).Error; err != nil {
			t.Fatalf("insert %s: %v", path, err)
		}
	}
	f1, f2, f3 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	botA, botB := uuid.NewString(), uuid.NewString()
	ins(f1, botA, "MEMORY.md", "用户偏好 dark mode，喜欢简单系统。issue INT-102 决策已定。")
	ins(f2, botA, "USER.md", "Bobo prefers concise answers and simple tools")
	ins(f3, botB, "MEMORY.md", "别的机器人的 dark mode 记忆不应被检索到")

	var hits []string
	q := func(match string) []string {
		hits = hits[:0]
		if err := db.Raw(`SELECT path FROM bot_memory_fts WHERE bot_id = ? AND body MATCH ? ORDER BY rank`,
			botA, match).Scan(&hits).Error; err != nil {
			t.Fatalf("match %q: %v", match, err)
		}
		return hits
	}

	if got := q(`"dark mode"`); len(got) != 1 || got[0] != "MEMORY.md" {
		t.Fatalf("english phrase: got %v", got)
	}
	if got := q(`"简单系统"`); len(got) != 1 || got[0] != "MEMORY.md" {
		t.Fatalf("chinese keyword: got %v", got)
	}
	if got := q(`"INT-102"`); len(got) != 1 {
		t.Fatalf("identifier: got %v", got)
	}
	// 隔离：bot B 的内容不能出现在 bot A 的检索里（SQL 层限定）。
	var cnt int64
	if err := db.Raw(`SELECT COUNT(*) FROM bot_memory_fts WHERE bot_id = ? AND body MATCH ?`,
		botA, `"别的机器人"`).Scan(&cnt).Error; err != nil {
		t.Fatalf("isolation: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("isolation leaked %d hits", cnt)
	}

	// snippet 与 bm25 可用性。
	var frag string
	var rank float64
	if err := db.Raw(`SELECT snippet(bot_memory_fts, 3, '[', ']', '…', 12), bm25(bot_memory_fts)
		FROM bot_memory_fts WHERE bot_id = ? AND body MATCH ? LIMIT 1`,
		botA, `"简单系统"`).Row().Scan(&frag, &rank); err != nil {
		t.Fatalf("snippet/bm25: %v", err)
	}
	if frag == "" {
		t.Fatalf("empty snippet")
	}
	t.Logf("snippet=%q rank=%v", frag, rank)

	// UPDATE/DELETE 行维护（模拟触发器同步）。
	if err := db.Exec(`DELETE FROM bot_memory_fts WHERE file_id = ?`, f1).Error; err != nil {
		t.Fatalf("delete row: %v", err)
	}
	if got := q(`"dark mode"`); len(got) != 0 {
		t.Fatalf("after delete: got %v", got)
	}
}
