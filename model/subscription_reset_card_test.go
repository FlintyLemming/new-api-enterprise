package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedResetCardUser 创建核销/发放测试所需的用户行（Grant 会校验用户存在）。
func seedResetCardUser(t *testing.T, id int) {
	t.Helper()
	require.NoError(t, DB.Create(&User{
		Id:       id,
		Username: fmt.Sprintf("reset-card-user-%d", id),
		Password: "password",
		Status:   common.UserStatusEnabled,
	}).Error)
}

// seedResetCard 直接插入一张重置卡。
func seedResetCard(t *testing.T, card *SubscriptionResetCard) {
	t.Helper()
	require.NoError(t, DB.Create(card).Error)
}

func getResetCard(t *testing.T, id int) SubscriptionResetCard {
	t.Helper()
	var card SubscriptionResetCard
	require.NoError(t, DB.Where("id = ?", id).First(&card).Error)
	return card
}

func TestSubscriptionResetCardTableRoundTrip(t *testing.T) {
	truncateTables(t)

	now := common.GetTimestamp()
	card := &SubscriptionResetCard{
		Name:        "9月补偿卡",
		UserId:      101,
		CreatedTime: now,
		ExpiredTime: 0,
	}
	require.NoError(t, DB.Create(card).Error)
	require.NotZero(t, card.Id)
	assert.Equal(t, common.SubscriptionResetCardStatusUnused, card.Status, "status 应走 GORM default:1")

	loaded := getResetCard(t, card.Id)
	assert.Equal(t, "9月补偿卡", loaded.Name)
	assert.Equal(t, 101, loaded.UserId)
	assert.Zero(t, loaded.UsedTime)
	assert.Zero(t, loaded.ExpiredTime)
	assert.Zero(t, loaded.UsedSubscriptionId)
}
