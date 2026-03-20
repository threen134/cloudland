package model

import (
	"time"

	"api/src/dbs"

	"github.com/jinzhu/gorm"
)

func init() {
	dbs.AutoMigrate(
		&NotificationChannel{},
		&AlarmNotificationBinding{},
		&AlarmEvent{},
		&AlarmDeliveryLog{},
	)

	// 清理可能存在的孤立绑定记录，并添加外键约束保证引用完整性
	dbs.AutoUpgrade("add_alarm_notification_binding_fk", func(db *gorm.DB) error {
		// 先清理孤立的绑定记录（channel 已不存在）
		if err := db.Exec(`DELETE FROM alarm_notification_bindings WHERE channel_uuid NOT IN (SELECT uuid FROM notification_channels)`).Error; err != nil {
			return err
		}
		// 添加外键约束：删除渠道时级联清理绑定
		return db.Model(&AlarmNotificationBinding{}).AddForeignKey(
			"channel_uuid", "notification_channels(uuid)", "CASCADE", "CASCADE",
		).Error
	})
}

// NotificationChannel 通知渠道镜像表（CPGateway 单向下发，clapi 只读使用）
type NotificationChannel struct {
	Model
	UserID  int64  `gorm:"index" json:"user_id"`
	Name    string `gorm:"type:varchar(128)" json:"name"`
	Type    string `gorm:"type:varchar(32)" json:"type"` // feishu, webhook
	Config  string `gorm:"type:text" json:"config"`      // JSON 配置
	Enabled bool   `gorm:"default:true" json:"enabled"`
}

// AlarmNotificationBinding 告警规则与渠道绑定关系表
type AlarmNotificationBinding struct {
	Model
	RuleGroupUUID string `gorm:"type:varchar(64);uniqueIndex:idx_rule_channel" json:"rule_group_uuid"`
	ChannelUUID   string `gorm:"type:varchar(64);uniqueIndex:idx_rule_channel" json:"channel_uuid"`
	UserID        int64  `gorm:"index" json:"user_id"` // 安全防御字段，防止越权绑定
}

// AlarmEvent 告警事件记录表
// Fingerprint 来自 AlertManager，作为同一告警的幂等键。
// 同一条告警从 firing 到 resolved 共享同一行记录。
type AlarmEvent struct {
	Model
	Fingerprint   string     `gorm:"type:varchar(128);uniqueIndex" json:"fingerprint"`
	RuleGroupUUID string     `gorm:"type:varchar(64);index" json:"rule_group_uuid"`
	AlertName     string     `gorm:"type:varchar(128)" json:"alert_name"`
	Owner         string     `gorm:"type:varchar(255);index" json:"owner"`
	VMUUID        string     `gorm:"column:vm_uuid;type:varchar(64);index" json:"vm_uuid"`
	VMName        string     `gorm:"type:varchar(128)" json:"vm_name"`
	Severity      string     `gorm:"type:varchar(32)" json:"severity"`
	Status        string     `gorm:"type:varchar(32)" json:"status"` // firing, resolved
	Summary       string     `gorm:"type:text" json:"summary"`
	Labels        string     `gorm:"type:text" json:"labels"` // JSON
	FiredAt       time.Time  `json:"fired_at"`
	LastFiredAt   time.Time  `json:"last_fired_at"`
	ResolvedAt    *time.Time `json:"resolved_at"`
}

// AlarmDeliveryLog 通知发送流水记录（只追加，不覆盖）
type AlarmDeliveryLog struct {
	Model
	EventUUID    string    `gorm:"type:varchar(64);index" json:"event_uuid"`
	ChannelUUID  string    `gorm:"type:varchar(64)" json:"channel_uuid"`
	ChannelName  string    `gorm:"type:varchar(128)" json:"channel_name"`
	ChannelType  string    `gorm:"type:varchar(32)" json:"channel_type"`
	NotifyType   string    `gorm:"type:varchar(32)" json:"notify_type"` // firing_trigger, repeat_remind, resolved
	Status       string    `gorm:"type:varchar(32)" json:"status"`      // sent, failed
	ErrorMessage string    `gorm:"type:text" json:"error_message"`
	SentAt       time.Time `json:"sent_at"`
}
