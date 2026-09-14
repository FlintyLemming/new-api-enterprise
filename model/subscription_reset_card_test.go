package model

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

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
		AffCode:  fmt.Sprintf("reset-card-aff-%d", id), // aff_code 有 uniqueIndex，同测试多用户需区分
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

// seedResetCardFixture 建一个 daily 套餐 + 一个 active 订阅，返回 plan/sub。
func seedResetCardFixture(t *testing.T, userId, planId, subId int, quotaResetPeriod string) (*SubscriptionPlan, *UserSubscription) {
	t.Helper()
	now := GetDBTimestamp()
	plan := &SubscriptionPlan{
		Id:               planId,
		Title:            "Pro",
		PriceAmount:      10,
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		TotalAmount:      1000,
		QuotaResetPeriod: quotaResetPeriod,
	}
	seedSubscriptionResetPlan(t, plan)
	sub := &UserSubscription{
		Id:            subId,
		UserId:        userId,
		PlanId:        planId,
		AmountTotal:   1000,
		AmountUsed:    300,
		StartTime:     now - 3600,
		EndTime:       now + 30*24*3600,
		Status:        "active",
		LastResetTime: now - 3600,
		NextResetTime: now + 120,
	}
	seedSubscriptionResetSub(t, sub)
	return plan, sub
}

func TestUseSubscriptionResetCardSuccess(t *testing.T) {
	truncateTables(t)
	seedResetCardUser(t, 101)
	plan, sub := seedResetCardFixture(t, 101, 9101, 9201, SubscriptionResetDaily)

	now := GetDBTimestamp()
	seedResetCard(t, &SubscriptionResetCard{Id: 1, Name: "卡A", UserId: 101, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})

	before := GetDBTimestamp()
	card, err := UseSubscriptionResetCard(101, sub.Id)
	after := GetDBTimestamp()

	require.NoError(t, err)
	require.NotNil(t, card)
	assert.Equal(t, 1, card.Id)
	assert.Equal(t, common.SubscriptionResetCardStatusUsed, card.Status)
	assert.Equal(t, sub.Id, card.UsedSubscriptionId)
	assert.GreaterOrEqual(t, card.UsedTime, before)
	assert.LessOrEqual(t, card.UsedTime, after)

	updated := getSubscriptionResetSub(t, sub.Id)
	assert.Zero(t, updated.AmountUsed, "已用额度应清零")
	assert.GreaterOrEqual(t, updated.LastResetTime, before, "advance 语义：LastResetTime 应刷为当前时刻")
	assert.LessOrEqual(t, updated.LastResetTime, after)
	assert.Equal(t, calcNextResetTime(time.Unix(updated.LastResetTime, 0), plan, updated.EndTime), updated.NextResetTime, "NextResetTime 应从当前时刻重算")

	loaded := getResetCard(t, 1)
	assert.Equal(t, common.SubscriptionResetCardStatusUsed, loaded.Status)
	assert.Equal(t, sub.Id, loaded.UsedSubscriptionId)
}

func TestUseSubscriptionResetCardNeverPlan(t *testing.T) {
	truncateTables(t)
	seedResetCardUser(t, 102)
	_, sub := seedResetCardFixture(t, 102, 9102, 9202, SubscriptionResetNever)

	now := GetDBTimestamp()
	seedResetCard(t, &SubscriptionResetCard{Id: 2, Name: "卡B", UserId: 102, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})

	_, err := UseSubscriptionResetCard(102, sub.Id)
	require.NoError(t, err, "never 套餐订阅也可核销")

	updated := getSubscriptionResetSub(t, sub.Id)
	assert.Zero(t, updated.AmountUsed)
	assert.Zero(t, updated.NextResetTime, "never 套餐不重启周期")
	assert.Zero(t, updated.LastResetTime)
}

func TestUseSubscriptionResetCardFIFOOrder(t *testing.T) {
	truncateTables(t)
	seedResetCardUser(t, 103)
	_, sub := seedResetCardFixture(t, 103, 9103, 9203, SubscriptionResetDaily)

	now := GetDBTimestamp()
	seedResetCard(t, &SubscriptionResetCard{Id: 3, Name: "不过期", UserId: 103, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})
	seedResetCard(t, &SubscriptionResetCard{Id: 4, Name: "晚过期", UserId: 103, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: now + 7200})
	seedResetCard(t, &SubscriptionResetCard{Id: 5, Name: "早过期", UserId: 103, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: now + 3600})

	card, err := UseSubscriptionResetCard(103, sub.Id)
	require.NoError(t, err)
	assert.Equal(t, 5, card.Id, "先消耗最早过期的卡")

	card, err = UseSubscriptionResetCard(103, sub.Id)
	require.NoError(t, err)
	assert.Equal(t, 4, card.Id)

	card, err = UseSubscriptionResetCard(103, sub.Id)
	require.NoError(t, err)
	assert.Equal(t, 3, card.Id, "不过期的卡排最后")

	_, err = UseSubscriptionResetCard(103, sub.Id)
	require.ErrorIs(t, err, ErrNoAvailableResetCard)
}

func TestUseSubscriptionResetCardFailures(t *testing.T) {
	truncateTables(t)
	seedResetCardUser(t, 104)
	seedResetCardUser(t, 105)
	_, sub := seedResetCardFixture(t, 104, 9104, 9204, SubscriptionResetDaily)
	now := GetDBTimestamp()

	// 无卡
	_, err := UseSubscriptionResetCard(104, sub.Id)
	require.ErrorIs(t, err, ErrNoAvailableResetCard)

	// 只有已过期的卡
	seedResetCard(t, &SubscriptionResetCard{Id: 6, Name: "已过期", UserId: 104, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now - 7200, ExpiredTime: now - 1})
	_, err = UseSubscriptionResetCard(104, sub.Id)
	require.ErrorIs(t, err, ErrNoAvailableResetCard, "过期卡不可用")

	// 订阅不属于该用户
	seedResetCard(t, &SubscriptionResetCard{Id: 7, Name: "卡C", UserId: 105, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})
	_, err = UseSubscriptionResetCard(105, sub.Id)
	require.ErrorIs(t, err, ErrResetCardInvalidSubscription)
	assert.Equal(t, common.SubscriptionResetCardStatusUnused, getResetCard(t, 7).Status, "失败回滚后卡不应被消耗")

	// 订阅已过期（end_time 已过）
	seedResetCard(t, &SubscriptionResetCard{Id: 8, Name: "卡D", UserId: 104, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9205, UserId: 104, PlanId: 9104, AmountTotal: 1000, AmountUsed: 100, StartTime: now - 7200, EndTime: now - 1, Status: "active"})
	_, err = UseSubscriptionResetCard(104, 9205)
	require.ErrorIs(t, err, ErrResetCardInvalidSubscription)
	assert.Equal(t, common.SubscriptionResetCardStatusUnused, getResetCard(t, 8).Status)

	// 订阅已作废（cancelled）
	seedSubscriptionResetSub(t, &UserSubscription{Id: 9206, UserId: 104, PlanId: 9104, AmountTotal: 1000, AmountUsed: 100, StartTime: now - 7200, EndTime: now + 86400, Status: "cancelled"})
	_, err = UseSubscriptionResetCard(104, 9206)
	require.ErrorIs(t, err, ErrResetCardInvalidSubscription)
	assert.Equal(t, common.SubscriptionResetCardStatusUnused, getResetCard(t, 8).Status)
}

// 同一用户只有一张卡时并发核销，只允许一个事务成功（对齐 TestRedeemConcurrentSingleSuccess）。
func TestUseSubscriptionResetCardConcurrentSingleSuccess(t *testing.T) {
	truncateTables(t)
	seedResetCardUser(t, 106)
	_, sub := seedResetCardFixture(t, 106, 9106, 9207, SubscriptionResetDaily)

	now := GetDBTimestamp()
	seedResetCard(t, &SubscriptionResetCard{Id: 9, Name: "卡E", UserId: 106, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})

	const goroutines = 5
	successes := make([]bool, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			if _, err := UseSubscriptionResetCard(106, sub.Id); err == nil {
				successes[idx] = true
			}
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, ok := range successes {
		if ok {
			successCount++
		}
	}
	assert.Equal(t, 1, successCount, "并发核销同一用户唯一的卡只能成功一次")
	assert.Equal(t, common.SubscriptionResetCardStatusUsed, getResetCard(t, 9).Status)
	assert.Zero(t, getSubscriptionResetSub(t, sub.Id).AmountUsed)
}

func TestCountAvailableSubscriptionResetCards(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()

	seedResetCard(t, &SubscriptionResetCard{Id: 10, UserId: 107, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})
	seedResetCard(t, &SubscriptionResetCard{Id: 11, UserId: 107, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: now + 3600})
	seedResetCard(t, &SubscriptionResetCard{Id: 12, UserId: 107, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now - 7200, ExpiredTime: now - 1})
	seedResetCard(t, &SubscriptionResetCard{Id: 13, UserId: 107, Status: common.SubscriptionResetCardStatusUsed, CreatedTime: now, ExpiredTime: 0, UsedTime: now, UsedSubscriptionId: 1})
	seedResetCard(t, &SubscriptionResetCard{Id: 14, UserId: 107, Status: common.SubscriptionResetCardStatusDisabled, CreatedTime: now, ExpiredTime: 0})
	seedResetCard(t, &SubscriptionResetCard{Id: 15, UserId: 108, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})

	count, err := CountAvailableSubscriptionResetCards(107)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count, "只统计未使用且未过期的卡")
}

func TestSearchSubscriptionResetCardsFilters(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()

	seedResetCard(t, &SubscriptionResetCard{Id: 20, Name: "补偿卡", UserId: 107, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})
	seedResetCard(t, &SubscriptionResetCard{Id: 21, Name: "补偿卡-过期", UserId: 108, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now - 7200, ExpiredTime: now - 1})
	seedResetCard(t, &SubscriptionResetCard{Id: 22, Name: "活动卡", UserId: 107, Status: common.SubscriptionResetCardStatusUsed, CreatedTime: now, ExpiredTime: 0, UsedTime: now, UsedSubscriptionId: 1})

	// 过期虚拟态：只含未使用且已过期的卡
	cards, total, err := SearchSubscriptionResetCards("", "expired", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, cards, 1)
	assert.Equal(t, 21, cards[0].Id)

	// 未使用态排除已过期
	_, total, err = SearchSubscriptionResetCards("", strconv.Itoa(common.SubscriptionResetCardStatusUnused), 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)

	// 已使用态
	_, total, err = SearchSubscriptionResetCards("", strconv.Itoa(common.SubscriptionResetCardStatusUsed), 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)

	// 关键字：数字匹配卡 ID / 用户 ID；名称前缀
	_, total, err = SearchSubscriptionResetCards("补偿", "", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	_, total, err = SearchSubscriptionResetCards("108", "", 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "数字关键字应命中 user_id=108 的卡")

	// 列表 ID 倒序
	cards, total, err = GetAllSubscriptionResetCards(0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, cards, 3)
	assert.Equal(t, 22, cards[0].Id)
}

func TestSubscriptionResetCardListsFillUsername(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()

	seedResetCardUser(t, 130)
	seedResetCard(t, &SubscriptionResetCard{Id: 30, Name: "补偿卡", UserId: 130, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})
	// 131 号用户不存在（已删除），用户名应留空，由前端回退到用户 ID
	seedResetCard(t, &SubscriptionResetCard{Id: 31, Name: "补偿卡-无主", UserId: 131, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})

	cards, _, err := GetAllSubscriptionResetCards(0, 10)
	require.NoError(t, err)
	require.Len(t, cards, 2)
	assert.Equal(t, "", cards[0].Username, "用户已删除时用户名为空")
	assert.Equal(t, "reset-card-user-130", cards[1].Username)

	cards, _, err = SearchSubscriptionResetCards("130", "", 0, 10)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, "reset-card-user-130", cards[0].Username)
}

func TestDisableSubscriptionResetCards(t *testing.T) {
	truncateTables(t)
	seedResetCardUser(t, 109)
	_, sub := seedResetCardFixture(t, 109, 9109, 9208, SubscriptionResetDaily)
	now := GetDBTimestamp()

	seedResetCard(t, &SubscriptionResetCard{Id: 30, UserId: 109, Status: common.SubscriptionResetCardStatusUnused, CreatedTime: now, ExpiredTime: 0})
	seedResetCard(t, &SubscriptionResetCard{Id: 31, UserId: 109, Status: common.SubscriptionResetCardStatusUsed, CreatedTime: now, ExpiredTime: 0, UsedTime: now, UsedSubscriptionId: sub.Id})

	// 已使用的卡不可禁用
	_, err := DisableSubscriptionResetCards([]int{31})
	require.ErrorIs(t, err, ErrResetCardNotUnused)
	assert.Equal(t, common.SubscriptionResetCardStatusUsed, getResetCard(t, 31).Status)

	// 不存在的卡
	_, err = DisableSubscriptionResetCards([]int{9999})
	require.ErrorIs(t, err, ErrResetCardNotFound)

	// 空批量 / 超上限
	_, err = DisableSubscriptionResetCards(nil)
	require.Error(t, err)
	_, err = DisableSubscriptionResetCards(make([]int, 1001))
	require.Error(t, err)

	// 正常禁用，幂等二次禁用报错
	affected, err := DisableSubscriptionResetCards([]int{30})
	require.NoError(t, err)
	assert.EqualValues(t, 1, affected)
	assert.Equal(t, common.SubscriptionResetCardStatusDisabled, getResetCard(t, 30).Status)
	_, err = DisableSubscriptionResetCards([]int{30})
	require.ErrorIs(t, err, ErrResetCardNotUnused)

	// 禁用后不可核销
	_, err = UseSubscriptionResetCard(109, sub.Id)
	require.ErrorIs(t, err, ErrNoAvailableResetCard)
}

func TestDeleteSubscriptionResetCardById(t *testing.T) {
	truncateTables(t)
	now := GetDBTimestamp()
	seedResetCard(t, &SubscriptionResetCard{Id: 40, UserId: 110, Status: common.SubscriptionResetCardStatusUsed, CreatedTime: now, ExpiredTime: 0, UsedTime: now, UsedSubscriptionId: 1})

	require.NoError(t, DeleteSubscriptionResetCardById(40))

	_, total, err := GetAllSubscriptionResetCards(0, 10)
	require.NoError(t, err)
	assert.Zero(t, total, "软删除后管理端列表不可见")

	// 软删除记录仍在（用户日志追溯不受影响）
	var raw SubscriptionResetCard
	require.NoError(t, DB.Unscoped().Where("id = ?", 40).First(&raw).Error)

	require.Error(t, DeleteSubscriptionResetCardById(40), "重复删除应报错")
	require.Error(t, DeleteSubscriptionResetCardById(0))
}
