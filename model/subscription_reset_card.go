package model

import (
	"errors"

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
