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

func TestGrantSubscriptionResetCardsCountBounds(t *testing.T) {
	truncateTables(t)
	seedResetCardUser(t, 101)

	for _, count := range []int{0, -1, 101, 1000} {
		err := GrantSubscriptionResetCards(101, "测试卡", count, 0)
		require.Error(t, err, "count=%d 应被拒绝", count)
	}
	for _, count := range []int{1, 100} {
		require.NoError(t, GrantSubscriptionResetCards(101, "测试卡", count, 0), "count=%d 应通过", count)
	}

	var total int64
	require.NoError(t, DB.Model(&SubscriptionResetCard{}).Where("user_id = ?", 101).Count(&total).Error)
	assert.EqualValues(t, 101, total)
}

func TestGrantSubscriptionResetCardsFields(t *testing.T) {
	truncateTables(t)
	seedResetCardUser(t, 102)

	before := common.GetTimestamp()
	require.NoError(t, GrantSubscriptionResetCards(102, "9月补偿卡", 2, before+86400))
	after := common.GetTimestamp()

	var cards []SubscriptionResetCard
	require.NoError(t, DB.Where("user_id = ?", 102).Order("id asc").Find(&cards).Error)
	require.Len(t, cards, 2)
	for _, card := range cards {
		assert.Equal(t, "9月补偿卡", card.Name)
		assert.Equal(t, common.SubscriptionResetCardStatusUnused, card.Status)
		assert.EqualValues(t, before+86400, card.ExpiredTime)
		assert.GreaterOrEqual(t, card.CreatedTime, before)
		assert.LessOrEqual(t, card.CreatedTime, after)
		assert.Zero(t, card.UsedTime)
		assert.Zero(t, card.UsedSubscriptionId)
	}
}

func TestGrantSubscriptionResetCardsUnknownUser(t *testing.T) {
	truncateTables(t)

	err := GrantSubscriptionResetCards(999, "测试卡", 1, 0)
	require.Error(t, err)
}
