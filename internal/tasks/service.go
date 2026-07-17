package tasks

import (
	"fmt"
	"log"
	"time"

	"visualink/internal/db"
	"visualink/internal/model"
	"visualink/internal/platform/hub"
)

// StartAutoArchive 启动自动归档后台任务(原 main 里的匿名 goroutine):
// 每小时扫描一次,将 done 超过 24h 的功能归档并通知创建者。
func StartAutoArchive(database *db.DB) {
	go func() {
		for {
			time.Sleep(1 * time.Hour)
			archived, err := database.AutoArchiveFeatures()
			if err != nil {
				log.Println("auto-archive error:", err)
				continue
			}
			if len(archived) == 0 {
				continue
			}
			seen := map[int64]bool{}
			for _, f := range archived {
				if err := database.CreateNotification(&model.Notification{
					UserID:       f.CreatedBy,
					FeatureID:    f.ID,
					FromUser:     "系统",
					FeatureTitle: f.Title,
					Message:      model.FeatureStatusNotificationText(f.Title, "archived", true),
				}); err != nil {
					log.Printf("auto-archive notification error for feature %d: %v", f.ID, err)
					continue
				}
				if !seen[f.CreatedBy] {
					seen[f.CreatedBy] = true
					hub.Global.Broadcast(fmt.Sprintf("mailbox-updated:%d", f.CreatedBy))
				}
			}
			log.Printf("auto-archived %d feature(s)", len(archived))
			hub.Global.Broadcast("feature-list-changed")
			hub.Global.Broadcast("stats-updated")
		}
	}()
}
