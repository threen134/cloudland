package model

import (
	"time"

	"api/src/dbs"

	"gorm.io/gorm"
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
		// 旧同步逻辑重复下发已软删除的渠道时会插入同 uuid 的新行；建唯一索引前每个 uuid 只保留一行（优先未删除，其次最新）
		if err := db.Exec(`
			DELETE FROM notification_channels a
			USING notification_channels b
			WHERE a.uuid = b.uuid AND a.id <> b.id
			AND ((a.deleted_at IS NOT NULL AND b.deleted_at IS NULL)
				OR ((a.deleted_at IS NULL) = (b.deleted_at IS NULL) AND a.id < b.id))
		`).Error; err != nil {
			return err
		}
		// 外键要求被引用列唯一，uuid 只有普通索引，先补唯一索引
		if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_channels_uuid_unique ON notification_channels (uuid)`).Error; err != nil {
			return err
		}
		// 添加外键约束：删除渠道时级联清理绑定（GORM v2 不再提供 AddForeignKey，改用 raw SQL）
		return db.Exec(`
			DO $$ BEGIN
				IF NOT EXISTS (
					SELECT 1 FROM information_schema.table_constraints
					WHERE constraint_name = 'fk_alarm_notification_bindings_channel'
					AND table_name = 'alarm_notification_bindings'
				) THEN
					ALTER TABLE alarm_notification_bindings
					ADD CONSTRAINT fk_alarm_notification_bindings_channel
					FOREIGN KEY (channel_uuid) REFERENCES notification_channels(uuid)
					ON DELETE CASCADE ON UPDATE CASCADE;
				END IF;
			END $$;
		`).Error
	})

	dbs.AutoUpgrade("alarm_events_owner_platform", AssignOwnerlessAlarmEvents)

	// Re-binding a channel to a rule group (saving the bindings again, or unbinding and binding back) failed with a
	// duplicate key: the old unique index covered the soft-deleted rows the replacement had just left behind
	dbs.AutoUpgrade("alarm_notification_binding_active_unique", func(db *gorm.DB) error {
		if err := db.Exec(`DROP INDEX IF EXISTS idx_rule_channel`).Error; err != nil {
			return err
		}
		return db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS idx_rule_channel_active
			ON alarm_notification_bindings (rule_group_uuid, channel_uuid)
			WHERE deleted_at IS NULL
		`).Error
	})
}

// AssignOwnerlessAlarmEvents gives the alarm events that have no owner to the system organization. Node alerts
// used to reach clapi without an owner label (the node rule templates had none), and the event list only
// shows an organization its own events: nobody saw them. They are about the platform, so they go to the
// system organization, as new ones do (services.alarmEventOwner). Idempotent; a no-op before the system
// organization exists.
func AssignOwnerlessAlarmEvents(db *gorm.DB) error {
	return db.Exec(`
		UPDATE alarm_events SET owner = CAST(o.id AS varchar(32))
		FROM (SELECT id FROM organizations WHERE org_type = ? AND deleted_at IS NULL ORDER BY id LIMIT 1) o
		WHERE alarm_events.owner = '' OR alarm_events.owner IS NULL
	`, OrgTypeSystem).Error
}

// NotificationChannel 通知渠道镜像表（CPGateway 单向下发，clapi 只读使用）
type NotificationChannel struct {
	Model
	// OrgUUID is the owning organization as the control plane names it; OrgID is that organization's
	// local ID in this region, resolved from OrgUUID. The two sides number their organizations
	// independently, so the control plane's ID must never be stored here. 0 means the organization
	// has not been synced to this region yet: such a channel belongs to nobody until it is resolved.
	OrgID   int64  `gorm:"index" json:"org_id"`
	OrgUUID string `gorm:"type:varchar(36);index" json:"org_uuid"`
	Name    string `gorm:"type:varchar(128)" json:"name"`
	Type    string `gorm:"type:varchar(32)" json:"type"` // feishu, webhook
	Config  string `gorm:"type:text" json:"config"`      // JSON 配置
	// No gorm default: on Create it would replace a deliberate false, so a channel created disabled
	// was mirrored as enabled and kept receiving notifications
	Enabled bool `json:"enabled"`
}

// AlarmNotificationBinding 告警规则与渠道绑定关系表
type AlarmNotificationBinding struct {
	Model
	// The pair is unique among live rows only (idx_rule_channel_active, see init): bindings are soft deleted,
	// and a unique index over all rows made saving the same binding again fail with a duplicate key
	RuleGroupUUID string `gorm:"type:varchar(64)" json:"rule_group_uuid"`
	ChannelUUID   string `gorm:"type:varchar(64);index" json:"channel_uuid"`
	OrgID         int64  `gorm:"index" json:"org_id"` // 租户隔离字段
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
