package notes

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// trashRetentionDays 回收站保留期:删除超过 30 天自动清除。
const trashRetentionDays = 30

// StartTrashAutoPurge 启动回收站到期清理后台任务:每 12 小时扫一次,
// 清掉删除超过保留期的笔记(库行 CASCADE + 磁盘附件目录)。
func StartTrashAutoPurge(d *Deps) {
	go func() {
		for {
			time.Sleep(12 * time.Hour)
			ids, err := d.Repo.PurgeExpiredNotes(trashRetentionDays)
			if err != nil {
				log.Println("trash auto-purge error:", err)
			}
			for _, id := range ids {
				_ = os.RemoveAll(filepath.Join(NoteAttachRoot, strconv.FormatInt(id, 10)))
			}
			if len(ids) > 0 {
				log.Printf("trash auto-purge: %d note(s) removed permanently", len(ids))
			}
		}
	}()
}
