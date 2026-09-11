package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// SubscriptionResetCard 订阅重置卡：管理员发放给用户，用户自助将名下某个有效订阅
// 当前周期的已用额度清零并重启重置周期。过期不是状态，只是查询过滤条件
// （expired_time != 0 且 < now 视为已过期，0 表示不过期）。
type SubscriptionResetCard struct {
	Id     int    `json:"id"`
	Name   string `json:"name"`
	UserId int    `json:"user_id" gorm:"index"`

	Status      int   `json:"status" gorm:"default:1"` // common.SubscriptionResetCardStatus*
	CreatedTime int64 `json:"created_time" gorm:"bigint"`
	UsedTime    int64 `json:"used_time" gorm:"bigint"`
	ExpiredTime int64 `json:"expired_time" gorm:"bigint"` // 0 = 不过期

	UsedSubscriptionId int `json:"used_subscription_id"` // 核销的订阅 ID，审计追溯用

	DeletedAt gorm.DeletedAt `gorm:"index"`
}

// maxSubscriptionResetCardsPerGrant 单次发放上限。
const maxSubscriptionResetCardsPerGrant = 100

// GrantSubscriptionResetCards 管理员向指定用户发放 count 张重置卡，同事务批量插入。
func GrantSubscriptionResetCards(userId int, name string, count int, expiredTime int64) error {
	if userId <= 0 {
		return errors.New("invalid user id")
	}
	if count < 1 || count > maxSubscriptionResetCardsPerGrant {
		return errors.New("count must be between 1 and 100")
	}
	now := common.GetTimestamp()
	return DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := tx.Select("id").First(&user, userId).Error; err != nil {
			return errors.New("user not found")
		}
		cards := make([]SubscriptionResetCard, count)
		for i := range cards {
			cards[i] = SubscriptionResetCard{
				Name:        name,
				UserId:      userId,
				Status:      common.SubscriptionResetCardStatusUnused,
				CreatedTime: now,
				ExpiredTime: expiredTime,
			}
		}
		return tx.Create(&cards).Error
	})
}

// UseSubscriptionResetCard 用户核销：FIFO 消耗一张可用卡，将指定订阅当前周期
// 已用额度清零并重启重置周期（advance 语义）。并发安全依赖 CAS 状态翻转，
// 不依赖行锁存在（SQLite 无行锁时也安全）。
func UseSubscriptionResetCard(userId, subscriptionId int) (*SubscriptionResetCard, error) {
	if userId <= 0 || subscriptionId <= 0 {
		return nil, errors.New("invalid user id or subscription id")
	}
	now := GetDBTimestamp()
	var card SubscriptionResetCard
	var sub UserSubscription
	var plan *SubscriptionPlan
	err := DB.Transaction(func(tx *gorm.DB) error {
		// FIFO：先消耗最早过期的可用卡，不过期（expired_time=0）的排最后。
		// CASE WHEN 写法在 SQLite/MySQL/PostgreSQL 上行为一致。
		err := lockForUpdate(tx).
			Where("user_id = ? AND status = ? AND (expired_time = 0 OR expired_time >= ?)",
				userId, common.SubscriptionResetCardStatusUnused, now).
			Order("CASE WHEN expired_time = 0 THEN 1 ELSE 0 END ASC, expired_time ASC, id ASC").
			First(&card).Error
		if err != nil {
			return ErrNoAvailableResetCard
		}
		// 事务内复核订阅：存在、属于该用户、处于有效状态。
		if err := lockForUpdate(tx).
			Where("id = ? AND user_id = ?", subscriptionId, userId).
			First(&sub).Error; err != nil {
			return ErrResetCardInvalidSubscription
		}
		if sub.Status != "active" || sub.EndTime <= now {
			return ErrResetCardInvalidSubscription
		}
		plan, err = getSubscriptionPlanByIdTx(tx, sub.PlanId)
		if err != nil {
			return err
		}
		// CAS：只有完成 status 1 -> 2 翻转的事务才算核销成功。
		result := tx.Model(&SubscriptionResetCard{}).
			Where("id = ? AND status = ?", card.Id, common.SubscriptionResetCardStatusUnused).
			Updates(map[string]any{
				"status":               common.SubscriptionResetCardStatusUsed,
				"used_time":            now,
				"used_subscription_id": subscriptionId,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrResetCardAlreadyUsed
		}
		return resetUserSubscriptionTx(tx, &sub, plan, now, true)
	})
	if err != nil {
		return nil, err
	}
	card.Status = common.SubscriptionResetCardStatusUsed
	card.UsedTime = now
	card.UsedSubscriptionId = subscriptionId
	RecordLog(userId, LogTypeManage, fmt.Sprintf("使用订阅重置卡（ID: %d）清零订阅（ID: %d，套餐 %s）当前周期已用额度并重启重置周期", card.Id, sub.Id, plan.Title))
	return &card, nil
}
