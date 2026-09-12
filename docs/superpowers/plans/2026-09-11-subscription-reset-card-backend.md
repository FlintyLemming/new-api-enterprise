# 订阅重置卡 Implementation Plan — Backend（Task 1–5）

> 本文件是 `docs/superpowers/plans/2026-09-11-subscription-reset-card.md` 的拆分文件。先读主计划（Global Constraints 对所有任务生效）与 spec `docs/superpowers/specs/2026-09-11-subscription-reset-card-design.md`。

**本部分目标：** 完成后端全部能力——`subscription_reset_cards` 表、发放/核销/禁用/删除/查询 model 函数、管理端与用户端 API。

---

### Task 1: 实体、常量、哨兵错误、AutoMigrate、测试夹具

**Files:**
- Create: `model/subscription_reset_card.go`
- Modify: `common/constants.go`（`RedemptionCodeStatus*` 块附近，约 line 249）
- Modify: `model/errors.go`
- Modify: `model/main.go:334`（`migrateDB()` 的 `DB.AutoMigrate` 列表）
- Modify: `model/task_cas_test.go`（`TestMain` 的 AutoMigrate 列表 + `truncateTables`）
- Test: `model/subscription_reset_card_test.go`（新建，后续 Task 2–4 继续追加）

**Interfaces:**
- Consumes: 既有 `common.RedemptionCodeStatus*` 常量风格、`model/task_cas_test.go` 测试夹具。
- Produces:
  - `type SubscriptionResetCard struct`（字段见下方代码）
  - `common.SubscriptionResetCardStatusUnused = 1`、`common.SubscriptionResetCardStatusUsed = 2`、`common.SubscriptionResetCardStatusDisabled = 3`
  - `model.ErrNoAvailableResetCard`、`model.ErrResetCardAlreadyUsed`、`model.ErrResetCardInvalidSubscription`、`model.ErrResetCardNotFound`、`model.ErrResetCardNotUnused`
  - 表名 `subscription_reset_cards`（GORM 默认复数命名）

- [ ] **Step 1: 写失败测试**

新建 `model/subscription_reset_card_test.go`：

```go
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
		Name:         "9月补偿卡",
		UserId:       101,
		CreatedTime:  now,
		ExpiredTime:  0,
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./model/ -run TestSubscriptionResetCardTableRoundTrip -v`
Expected: 编译失败，`undefined: SubscriptionResetCard`。

- [ ] **Step 3: 加状态常量**

`common/constants.go` 中 `RedemptionCodeStatusUsed = 3` 块之后追加：

```go
	// 订阅重置卡状态：1=未使用 2=已使用 3=已禁用（取值顺序与兑换码不同，勿混用）
	SubscriptionResetCardStatusUnused   = 1 // don't use 0, 0 is the default value!
	SubscriptionResetCardStatusUsed     = 2
	SubscriptionResetCardStatusDisabled = 3
```

- [ ] **Step 4: 加哨兵错误**

`model/errors.go` 中 `var ErrRedeemFailed = ...` 行之后追加：

```go
// Subscription reset card errors
var (
	ErrNoAvailableResetCard         = errors.New("no available subscription reset card")
	ErrResetCardAlreadyUsed         = errors.New("subscription reset card already used")
	ErrResetCardInvalidSubscription = errors.New("invalid subscription for reset card")
	ErrResetCardNotFound            = errors.New("subscription reset card not found")
	ErrResetCardNotUnused           = errors.New("subscription reset card is not unused")
)
```

- [ ] **Step 5: 建实体文件**

新建 `model/subscription_reset_card.go`：

```go
package model

import (
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
```

- [ ] **Step 6: 注册 AutoMigrate（生产 + 测试夹具）**

`model/main.go` `migrateDB()` 的 `DB.AutoMigrate(...)` 列表中，在 `&UserSubscription{},` 之后加一行 `&SubscriptionResetCard{},`。

`model/task_cas_test.go`：
- `TestMain` 的 `db.AutoMigrate(...)` 列表中，在 `&UserSubscription{},` 之后加 `&SubscriptionResetCard{},`；
- `truncateTables` 的 `t.Cleanup` 中加 `DB.Exec("DELETE FROM subscription_reset_cards")`。

- [ ] **Step 7: 跑测试确认通过**

Run: `go test ./model/ -run TestSubscriptionResetCardTableRoundTrip -v`
Expected: PASS。再跑 `gofmt -l model/ common/` 确认无输出。

- [ ] **Step 8: Commit**

```bash
git add model/subscription_reset_card.go model/subscription_reset_card_test.go common/constants.go model/errors.go model/main.go model/task_cas_test.go
git commit -m "feat(model): add SubscriptionResetCard entity, status constants and migration"
```

---

### Task 2: GrantSubscriptionResetCards（发放）

**Files:**
- Modify: `model/subscription_reset_card.go`
- Test: `model/subscription_reset_card_test.go`（追加）

**Interfaces:**
- Consumes: Task 1 的 `SubscriptionResetCard`、`common.SubscriptionResetCardStatusUnused`。
- Produces: `func GrantSubscriptionResetCards(userId int, name string, count int, expiredTime int64) error` — Task 5 的发放 handler 调用它。

- [ ] **Step 1: 写失败测试**

向 `model/subscription_reset_card_test.go` 追加：

```go
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./model/ -run TestGrantSubscriptionResetCards -v`
Expected: 编译失败，`undefined: GrantSubscriptionResetCards`。

- [ ] **Step 3: 实现发放**

`model/subscription_reset_card.go` 追加（import 增加 `errors` 与 `common`）：

```go
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
```

文件头部 import 变为：

```go
import (
	"errors"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./model/ -run TestGrantSubscriptionResetCards -v`
Expected: 3 个测试全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add model/subscription_reset_card.go model/subscription_reset_card_test.go
git commit -m "feat(model): add GrantSubscriptionResetCards with count bounds"
```

---

### Task 3: UseSubscriptionResetCard（核销核心）

**Files:**
- Modify: `model/subscription_reset_card.go`
- Test: `model/subscription_reset_card_test.go`（追加）

**Interfaces:**
- Consumes:
  - `resetUserSubscriptionTx(tx *gorm.DB, sub *UserSubscription, plan *SubscriptionPlan, now int64, advanceResetTime bool) error`（`model/subscription.go:1012`）
  - `getSubscriptionPlanByIdTx(tx *gorm.DB, id int) (*SubscriptionPlan, error)`（`model/subscription.go:389`）
  - `lockForUpdate(tx *gorm.DB) *gorm.DB`（`model/locking.go:20`）
  - `GetDBTimestamp() int64`（`model/db_time.go:7`）
  - `RecordLog(userId int, logType int, content string)`、`LogTypeManage`（`model/log.go`）
  - Task 1/2 的实体、常量、哨兵错误；测试夹具复用 `seedSubscriptionResetPlan` / `seedSubscriptionResetSub` / `getSubscriptionResetSub`（已在 `model/subscription_reset_test.go` 定义，同包可直接调用）。
- Produces: `func UseSubscriptionResetCard(userId, subscriptionId int) (*SubscriptionResetCard, error)` — Task 5 的用户端核销 handler 调用；返回的卡已翻成 Used 态（含 `UsedTime`、`UsedSubscriptionId`）。

核销语义（spec 4.1，必须严格实现）：
1. 开事务；
2. FIFO 取该用户最早过期的可用卡（`status=1 AND (expired_time=0 OR expired_time>=now)`，不过期的排最后），`lockForUpdate(tx)` 锁行；无卡 → `ErrNoAvailableResetCard`；
3. 事务内复核订阅：存在、属于该用户、`status='active'` 且 `end_time > now`；不满足 → `ErrResetCardInvalidSubscription`；随后加载套餐；
4. CAS 翻卡：`WHERE id=? AND status=1` 写 `status=2, used_time, used_subscription_id`；`RowsAffected==0` → `ErrResetCardAlreadyUsed`；
5. `resetUserSubscriptionTx(tx, &sub, plan, now, true)`；
6. 提交后 `RecordLog` 用户日志（卡 ID、订阅 ID、套餐名）。

- [ ] **Step 1: 写失败测试（成功路径 + FIFO）**

向 `model/subscription_reset_card_test.go` 追加（import 增加 `sync`、`time`）：

```go
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
```

- [ ] **Step 2: 写失败测试（失败路径 + 并发）**

继续追加：

```go
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
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./model/ -run TestUseSubscriptionResetCard -v`
Expected: 编译失败，`undefined: UseSubscriptionResetCard`。

- [ ] **Step 4: 实现核销**

`model/subscription_reset_card.go` 追加（import 增加 `fmt` 与 `github.com/QuantumNous/new-api/logger`）：

```go
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
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./model/ -run TestUseSubscriptionResetCard -v`
Expected: 5 个测试全部 PASS。再跑 `go test ./model/ -run 'TestAdminReset|TestRedeem' -v` 确认既有重置/兑换回归不破。

- [ ] **Step 6: Commit**

```bash
git add model/subscription_reset_card.go model/subscription_reset_card_test.go
git commit -m "feat(model): add UseSubscriptionResetCard with FIFO pick and CAS redemption"
```

---

### Task 4: 管理端 model 函数（列表/搜索/禁用/软删/可用数）

**Files:**
- Modify: `model/subscription_reset_card.go`
- Test: `model/subscription_reset_card_test.go`（追加）

**Interfaces:**
- Consumes: Task 1–3 的实体、常量、哨兵错误；`common.GetPageQuery` 的分页参数由 Task 5 控制器传入 `startIdx, num`。
- Produces（Task 5 全部用到）：
  - `func GetAllSubscriptionResetCards(startIdx, num int) (cards []*SubscriptionResetCard, total int64, err error)` — ID 倒序
  - `func SearchSubscriptionResetCards(keyword, status string, startIdx, num int) (cards []*SubscriptionResetCard, total int64, err error)` — `status` 支持 `"expired"` 虚拟态与 `"1"/"2"/"3"`；`keyword` 为数字时匹配卡 ID 或用户 ID，否则按名称前缀
  - `func DisableSubscriptionResetCards(ids []int) (int64, error)` — 逐张校验未使用，CAS 翻禁用；`ErrResetCardNotFound` / `ErrResetCardNotUnused`
  - `func DeleteSubscriptionResetCardById(id int) error` — 软删除
  - `func CountAvailableSubscriptionResetCards(userId int) (int64, error)` — 未使用且未过期

- [ ] **Step 1: 写失败测试**

向 `model/subscription_reset_card_test.go` 追加（import 增加 `strconv` 不需要——status 传字符串字面量即可）：

```go
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
```

注意测试文件 import 需补上 `strconv`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./model/ -run 'TestCountAvailable|TestSearchSubscriptionResetCards|TestDisableSubscriptionResetCards|TestDeleteSubscriptionResetCard' -v`
Expected: 编译失败，相关函数 undefined。

- [ ] **Step 3: 实现管理端 model 函数**

`model/subscription_reset_card.go` 追加（import 增加 `strconv`）：

```go
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
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./model/ -run 'TestCountAvailable|TestSearchSubscriptionResetCards|TestDisableSubscriptionResetCards|TestDeleteSubscriptionResetCard' -v`
Expected: 4 个测试全部 PASS。

- [ ] **Step 5: Commit**

```bash
git add model/subscription_reset_card.go model/subscription_reset_card_test.go
git commit -m "feat(model): add reset card admin queries, disable, soft delete, count"
```

---

### Task 5: 控制器、路由、后端 i18n、审计

**Files:**
- Create: `controller/subscription_reset_card.go`
- Modify: `i18n/keys.go`（`MsgRedemption*` 块之后）
- Modify: `i18n/locales/en.yaml`、`i18n/locales/zh-CN.yaml`（redemption.* 条目附近）
- Modify: `controller/audit.go`（`auditContentTemplates` map，约 line 60-65）
- Modify: `router/api-router.go:169-196`（subscription 路由组）

**Interfaces:**
- Consumes: Task 2/3/4 的 model 函数与哨兵错误；`common.ApiErrorI18n` / `common.ApiSuccess` / `common.GetPageQuery`（`common/gin.go`、`common/page_info.go`）；`recordManageAudit` / `recordManageAuditFor`（`controller/audit.go`）；`i18n.Msg*` 常量。
- Produces（前端 Task 6/9 依赖的 API 契约）：
  - `POST /api/subscription/admin/reset_cards/grant`，body `{user_id, name, count, expired_time}`（`expired_time=0` 不过期），响应 `ApiSuccess(null)`
  - `GET /api/subscription/admin/reset_cards/?p=&page_size=` → `data: {items, total, page, page_size}`
  - `GET /api/subscription/admin/reset_cards/search?keyword=&status=&p=&page_size=`（同上结构）
  - `POST /api/subscription/admin/reset_cards/disable`，body `{ids: number[]}` → `data: <禁用数量>`
  - `DELETE /api/subscription/admin/reset_cards/:id` → `ApiSuccess(null)`
  - `GET /api/subscription/self/reset_cards` → `data: {count: number}`
  - `POST /api/subscription/self/reset_cards/use`，body `{subscription_id}` → `data: {card_id, subscription_id}`；失败 `success:false`，`message` 为按用户语言翻译后的文案

- [ ] **Step 1: 加后端 i18n key**

`i18n/keys.go` 的 Redemption 消息块之后追加：

```go
// Subscription reset card related messages
const (
	MsgResetCardNoAvailable         = "subscription_reset_card.no_available"
	MsgResetCardAlreadyUsed         = "subscription_reset_card.already_used"
	MsgResetCardInvalidSubscription = "subscription_reset_card.invalid_subscription"
	MsgResetCardNotFound            = "subscription_reset_card.not_found"
	MsgResetCardNotUnused           = "subscription_reset_card.not_unused"
	MsgResetCardNameLength          = "subscription_reset_card.name_length"
	MsgResetCardCountRange          = "subscription_reset_card.count_range"
	MsgResetCardExpireTimeInvalid   = "subscription_reset_card.expire_time_invalid"
)
```

`i18n/locales/en.yaml` 追加：

```yaml
subscription_reset_card.no_available: "No available subscription reset card"
subscription_reset_card.already_used: "This reset card has already been used"
subscription_reset_card.invalid_subscription: "Subscription does not exist, does not belong to you, or is no longer active"
subscription_reset_card.not_found: "Subscription reset card not found"
subscription_reset_card.not_unused: "Only unused reset cards can be disabled"
subscription_reset_card.name_length: "Reset card name length must be between 1-50"
subscription_reset_card.count_range: "Grant count must be between 1-100"
subscription_reset_card.expire_time_invalid: "Expiration time cannot be earlier than current time"
```

`i18n/locales/zh-CN.yaml` 追加：

```yaml
subscription_reset_card.no_available: "没有可用的重置卡"
subscription_reset_card.already_used: "重置卡已被使用"
subscription_reset_card.invalid_subscription: "订阅不存在、不属于当前用户或已失效"
subscription_reset_card.not_found: "重置卡不存在"
subscription_reset_card.not_unused: "仅未使用的重置卡可以禁用"
subscription_reset_card.name_length: "重置卡名称长度必须在1-50之间"
subscription_reset_card.count_range: "单次发放数量必须在 1-100 之间"
subscription_reset_card.expire_time_invalid: "过期时间不能早于当前时间"
```

- [ ] **Step 2: 加审计模板**

`controller/audit.go` 的 `auditContentTemplates` map 中，`"subscription.user_plan_reset"` 行之后追加：

```go
	"subscription_reset_card.grant":   "Granted ${count} subscription reset cards named ${name} to user ${target_user_id}",
	"subscription_reset_card.disable": "Disabled ${count} subscription reset cards",
	"subscription_reset_card.delete":  "Deleted subscription reset card ${card_id}",
```

- [ ] **Step 3: 写控制器**

新建 `controller/subscription_reset_card.go`：

```go
package controller

import (
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// ---- Admin APIs ----

type adminGrantSubscriptionResetCardsRequest struct {
	UserId      int    `json:"user_id"`
	Name        string `json:"name"`
	Count       int    `json:"count"`
	ExpiredTime int64  `json:"expired_time"` // 0 = 不过期
}

func AdminGrantSubscriptionResetCards(c *gin.Context) {
	var req adminGrantSubscriptionResetCardsRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.UserId <= 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	if nameLen := utf8.RuneCountInString(req.Name); nameLen < 1 || nameLen > 50 {
		common.ApiErrorI18n(c, i18n.MsgResetCardNameLength)
		return
	}
	if req.Count < 1 || req.Count > 100 {
		common.ApiErrorI18n(c, i18n.MsgResetCardCountRange)
		return
	}
	if req.ExpiredTime != 0 && req.ExpiredTime < common.GetTimestamp() {
		common.ApiErrorI18n(c, i18n.MsgResetCardExpireTimeInvalid)
		return
	}
	if err := model.GrantSubscriptionResetCards(req.UserId, req.Name, req.Count, req.ExpiredTime); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAuditFor(c, req.UserId, "subscription_reset_card.grant", map[string]any{
		"name":  req.Name,
		"count": req.Count,
	})
	common.ApiSuccess(c, nil)
}

func AdminGetAllSubscriptionResetCards(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	cards, total, err := model.GetAllSubscriptionResetCards(pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(cards)
	common.ApiSuccess(c, pageInfo)
}

func AdminSearchSubscriptionResetCards(c *gin.Context) {
	keyword := c.Query("keyword")
	status := c.Query("status")
	pageInfo := common.GetPageQuery(c)
	cards, total, err := model.SearchSubscriptionResetCards(keyword, status, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(cards)
	common.ApiSuccess(c, pageInfo)
}

func AdminDisableSubscriptionResetCards(c *gin.Context) {
	var req struct {
		Ids []int `json:"ids" binding:"required,min=1,max=1000,dive,gt=0"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	count, err := model.DisableSubscriptionResetCards(req.Ids)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrResetCardNotFound):
			common.ApiErrorI18n(c, i18n.MsgResetCardNotFound)
		case errors.Is(err, model.ErrResetCardNotUnused):
			common.ApiErrorI18n(c, i18n.MsgResetCardNotUnused)
		default:
			common.ApiError(c, err)
		}
		return
	}
	recordManageAudit(c, "subscription_reset_card.disable", map[string]any{
		"count":    count,
		"card_ids": req.Ids,
	})
	common.ApiSuccess(c, count)
}

func AdminDeleteSubscriptionResetCard(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if id <= 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidId)
		return
	}
	if err := model.DeleteSubscriptionResetCardById(id); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "subscription_reset_card.delete", map[string]any{
		"card_id": id,
	})
	common.ApiSuccess(c, nil)
}

// ---- User APIs ----

func GetSelfSubscriptionResetCards(c *gin.Context) {
	userId := c.GetInt("id")
	count, err := model.CountAvailableSubscriptionResetCards(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"count": count})
}

type useSubscriptionResetCardRequest struct {
	SubscriptionId int `json:"subscription_id"`
}

func UseSelfSubscriptionResetCard(c *gin.Context) {
	userId := c.GetInt("id")
	var req useSubscriptionResetCardRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.SubscriptionId <= 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	card, err := model.UseSubscriptionResetCard(userId, req.SubscriptionId)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrNoAvailableResetCard):
			common.ApiErrorI18n(c, i18n.MsgResetCardNoAvailable)
		case errors.Is(err, model.ErrResetCardAlreadyUsed):
			common.ApiErrorI18n(c, i18n.MsgResetCardAlreadyUsed)
		case errors.Is(err, model.ErrResetCardInvalidSubscription):
			common.ApiErrorI18n(c, i18n.MsgResetCardInvalidSubscription)
		default:
			common.ApiError(c, err)
		}
		return
	}
	common.ApiSuccess(c, gin.H{
		"card_id":         card.Id,
		"subscription_id": card.UsedSubscriptionId,
	})
}
```

- [ ] **Step 4: 注册路由**

`router/api-router.go` 的 `subscriptionRoute` 块中，`subscriptionRoute.PUT("/self/preference", ...)` 行之后加：

```go
			subscriptionRoute.GET("/self/reset_cards", controller.GetSelfSubscriptionResetCards)
			subscriptionRoute.POST("/self/reset_cards/use", middleware.CriticalRateLimit(), controller.UseSelfSubscriptionResetCard)
```

`subscriptionAdminRoute` 块中，`subscriptionAdminRoute.DELETE("/user_subscriptions/:id", ...)` 行之后加：

```go
			// Subscription reset cards (admin)
			subscriptionAdminRoute.POST("/reset_cards/grant", controller.AdminGrantSubscriptionResetCards)
			subscriptionAdminRoute.GET("/reset_cards/", controller.AdminGetAllSubscriptionResetCards)
			subscriptionAdminRoute.GET("/reset_cards/search", controller.AdminSearchSubscriptionResetCards)
			subscriptionAdminRoute.POST("/reset_cards/disable", controller.AdminDisableSubscriptionResetCards)
			subscriptionAdminRoute.DELETE("/reset_cards/:id", controller.AdminDeleteSubscriptionResetCard)
```

- [ ] **Step 5: 编译 + 全量 model 测试**

Run:

```bash
go build ./...
go vet ./controller/ ./model/ ./i18n/ ./router/
go test ./model/ -v -run 'SubscriptionResetCard|TestAdminReset|TestRedeem' 2>&1 | tail -30
```

Expected: 编译无错；所有重置卡测试 + 既有重置/兑换回归 PASS。`gofmt -l controller/ i18n/ router/` 无输出。

- [ ] **Step 6: 冒烟（可选但推荐）——用 SQLite 起服务验证路由**

```bash
go run . --port 3000 &
# 管理员 session 下调：
#   curl -X POST localhost:3000/api/subscription/admin/reset_cards/grant -H 'Cookie: session=...' -d '{"user_id":1,"name":"冒烟卡","count":1,"expired_time":0}'
#   curl localhost:3000/api/subscription/self/reset_cards -H 'Cookie: session=...'
# 期望返回 {"success":true,...,"data":{"count":1}}
kill %1
```

Expected: 两个接口返回 `success:true`。若跳过本步，必须在最终交付说明中注明。

- [ ] **Step 7: Commit**

```bash
git add controller/subscription_reset_card.go i18n/keys.go i18n/locales/en.yaml i18n/locales/zh-CN.yaml controller/audit.go router/api-router.go
git commit -m "feat(controller): add subscription reset card admin/user APIs with i18n errors"
```
