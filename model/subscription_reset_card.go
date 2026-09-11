package model

import (
	"errors"
	"fmt"
	"strconv"

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

// GetAllSubscriptionResetCards 管理端分页列表，ID 倒序。
func GetAllSubscriptionResetCards(startIdx, num int) (cards []*SubscriptionResetCard, total int64, err error) {
	if err = DB.Model(&SubscriptionResetCard{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err = DB.Order("id desc").Limit(num).Offset(startIdx).Find(&cards).Error
	return cards, total, err
}

// SearchSubscriptionResetCards 管理端搜索：keyword 为数字时匹配卡 ID 或用户 ID，
// 否则按名称前缀；status 支持 "expired" 虚拟态（未使用但已过期）与数字状态，
// 逻辑对齐 SearchRedemptions。
func SearchSubscriptionResetCards(keyword, status string, startIdx, num int) (cards []*SubscriptionResetCard, total int64, err error) {
	query := DB.Model(&SubscriptionResetCard{})

	if keyword != "" {
		if id, convErr := strconv.Atoi(keyword); convErr == nil {
			query = query.Where("id = ? OR user_id = ? OR name LIKE ?", id, id, keyword+"%")
		} else {
			query = query.Where("name LIKE ?", keyword+"%")
		}
	}

	if status != "" {
		now := common.GetTimestamp()
		switch status {
		case "expired":
			query = query.Where(
				"status = ? AND expired_time != 0 AND expired_time < ?",
				common.SubscriptionResetCardStatusUnused,
				now,
			)
		case strconv.Itoa(common.SubscriptionResetCardStatusUnused):
			query = query.Where(
				"status = ? AND (expired_time = 0 OR expired_time >= ?)",
				common.SubscriptionResetCardStatusUnused,
				now,
			)
		case strconv.Itoa(common.SubscriptionResetCardStatusUsed):
			query = query.Where("status = ?", common.SubscriptionResetCardStatusUsed)
		case strconv.Itoa(common.SubscriptionResetCardStatusDisabled):
			query = query.Where("status = ?", common.SubscriptionResetCardStatusDisabled)
		}
	}

	if err = query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err = query.Order("id desc").Limit(num).Offset(startIdx).Find(&cards).Error
	return cards, total, err
}

// DisableSubscriptionResetCards 批量禁用未使用的卡，逐张校验（1..1000 个 ID）。
// 更新带 status=未使用 条件，与核销 CAS 互斥：竞态下必有一方失败。
func DisableSubscriptionResetCards(ids []int) (int64, error) {
	if len(ids) == 0 || len(ids) > 1000 {
		return 0, errors.New("select between 1 and 1000 reset cards")
	}
	for _, id := range ids {
		if id <= 0 {
			return 0, errors.New("reset card IDs must be positive")
		}
	}
	var affected int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		for _, id := range ids {
			var card SubscriptionResetCard
			if err := lockForUpdate(tx).Where("id = ?", id).First(&card).Error; err != nil {
				return ErrResetCardNotFound
			}
			if card.Status != common.SubscriptionResetCardStatusUnused {
				return ErrResetCardNotUnused
			}
			result := tx.Model(&SubscriptionResetCard{}).
				Where("id = ? AND status = ?", id, common.SubscriptionResetCardStatusUnused).
				Update("status", common.SubscriptionResetCardStatusDisabled)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return ErrResetCardNotUnused
			}
			affected++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// DeleteSubscriptionResetCardById 软删除，只影响管理端展示。
func DeleteSubscriptionResetCardById(id int) error {
	if id <= 0 {
		return errors.New("id 为空！")
	}
	var card SubscriptionResetCard
	if err := DB.Where("id = ?", id).First(&card).Error; err != nil {
		return err
	}
	return DB.Delete(&card).Error
}

// CountAvailableSubscriptionResetCards 用户端：未使用且未过期的卡数量。
func CountAvailableSubscriptionResetCards(userId int) (int64, error) {
	var count int64
	now := common.GetTimestamp()
	err := DB.Model(&SubscriptionResetCard{}).
		Where("user_id = ? AND status = ? AND (expired_time = 0 OR expired_time >= ?)",
			userId, common.SubscriptionResetCardStatusUnused, now).
		Count(&count).Error
	return count, err
}
