package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var newUserSubscriptionDialects = []string{"sqlite", "mysql", "postgres"}

// setupNewUserSubscriptionTest points the model layer at an isolated database
// of the given dialect and restores every global it touches afterwards.
func setupNewUserSubscriptionTest(t *testing.T, dialect string) {
	t.Helper()
	dsn := ""
	if dialect != "sqlite" {
		dsn = os.Getenv("TEST_" + strings.ToUpper(dialect) + "_DSN")
		if dsn == "" {
			t.Skipf("TEST_%s_DSN is not configured", strings.ToUpper(dialect))
		}
	}
	require.NoError(t, i18n.Init())
	databaseTypes := map[string]common.DatabaseType{
		"sqlite": common.DatabaseTypeSQLite, "mysql": common.DatabaseTypeMySQL, "postgres": common.DatabaseTypePostgreSQL,
	}
	db, _ := newAuditTestDatabase(t, dialect, dsn)

	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	previousOptionMap := common.OptionMap
	paymentSetting := operation_setting.GetPaymentSetting()
	previousConfirmed, previousTerms := paymentSetting.ComplianceConfirmed, paymentSetting.ComplianceTermsVersion
	previousPlanId := operation_setting.GetSubscriptionSetting().NewUserPlanId
	// Registered before the cleanup below so it is restored after it: the
	// cleanup's InitLogDB must never see a developer's real LOG_SQL_DSN.
	t.Setenv("LOG_SQL_DSN", "")
	t.Cleanup(func() {
		// InitLogDB re-derives the dialect-specific column names (`group` vs
		// "group") for the restored database type while the test database is
		// still open; the previous handles are put back afterwards.
		common.SetDatabaseTypes(previousMainType, previousLogType)
		assert.NoError(t, model.InitLogDB())
		common.SetDatabaseTypes(previousMainType, previousLogType)
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled = previousRedis
		common.OptionMap = previousOptionMap
		paymentSetting.ComplianceConfirmed, paymentSetting.ComplianceTermsVersion = previousConfirmed, previousTerms
		operation_setting.GetSubscriptionSetting().NewUserPlanId = previousPlanId
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	model.DB = db
	common.SetDatabaseTypes(databaseTypes[dialect], databaseTypes[dialect])
	require.NoError(t, model.InitLogDB())
	common.RedisEnabled = false
	common.OptionMap = map[string]string{}
	paymentSetting.ComplianceConfirmed, paymentSetting.ComplianceTermsVersion = true, operation_setting.CurrentComplianceTermsVersion
	operation_setting.GetSubscriptionSetting().NewUserPlanId = 0

	require.NoError(t, db.AutoMigrate(&model.User{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.Option{}, &model.Log{}, &model.AuditLog{}))
	versionQuery := "SELECT version()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database: %s %s", dialect, version)
}

// newUserPlanQuota gives every dialect its own plan quota. The plan cache is
// process-wide and keyed by id, and each isolated database restarts ids at 1,
// so a plan read served from another dialect's run shows up as a wrong quota.
var newUserPlanQuota = map[string]int64{"sqlite": 150000000, "mysql": 150000001, "postgres": 150000002}

// seedNewUserPlan stores the shape of the production "5h 150M" plan: a 100-year
// plan that resets every 18000 seconds and is hidden from the store (disabled).
func seedNewUserPlan(t *testing.T, title string, upgradeGroup string, totalAmount int64) *model.SubscriptionPlan {
	t.Helper()
	plan := &model.SubscriptionPlan{
		Title:                   title,
		DurationUnit:            model.SubscriptionDurationYear,
		DurationValue:           100,
		TotalAmount:             totalAmount,
		QuotaResetPeriod:        model.SubscriptionResetCustom,
		QuotaResetCustomSeconds: 18000,
		UpgradeGroup:            upgradeGroup,
	}
	require.NoError(t, model.DB.Create(plan).Error)
	require.NoError(t, model.DB.Model(plan).Update("enabled", false).Error)
	model.InvalidateSubscriptionPlanCache(plan.Id)
	return plan
}

func newUserSubscriptions(t *testing.T, userId int) []model.UserSubscription {
	t.Helper()
	var subs []model.UserSubscription
	require.NoError(t, model.DB.Where("user_id = ?", userId).Find(&subs).Error)
	return subs
}

type subscriptionSettingsResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		NewUserPlanId int `json:"new_user_plan_id"`
	} `json:"data"`
}

func callSubscriptionSettings(t *testing.T, handler gin.HandlerFunc, method string, body string) subscriptionSettingsResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, "/api/subscription/admin/settings", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)
	c.Set("username", "root")
	c.Set("role", common.RoleRootUser)
	handler(c)
	var response subscriptionSettingsResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response
}

func storedNewUserPlanOption(t *testing.T) string {
	t.Helper()
	var option model.Option
	err := model.DB.Where(&model.Option{Key: operation_setting.SubscriptionNewUserPlanIdOptionKey}).First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ""
	}
	require.NoError(t, err)
	return option.Value
}

func TestNewUserDefaultSubscriptionSettingsAPI(t *testing.T) {
	for _, dialect := range newUserSubscriptionDialects {
		t.Run(dialect, func(t *testing.T) {
			setupNewUserSubscriptionTest(t, dialect)
			plan := seedNewUserPlan(t, "5h150M", "", newUserPlanQuota[dialect])
			planIdText := strconv.Itoa(plan.Id)

			saved := callSubscriptionSettings(t, AdminUpdateSubscriptionSettings, http.MethodPut, `{"new_user_plan_id": `+planIdText+`}`)
			require.True(t, saved.Success, saved.Message)
			assert.Equal(t, plan.Id, saved.Data.NewUserPlanId)
			assert.Equal(t, plan.Id, operation_setting.GetSubscriptionSetting().NewUserPlanId)
			assert.Equal(t, planIdText, storedNewUserPlanOption(t))

			var audits []model.AuditLog
			require.NoError(t, model.LOG_DB.Where("action = ?", "subscription.settings_update").Find(&audits).Error)
			require.Len(t, audits, 1)
			require.NotNil(t, audits[0].Other.Op)
			params, err := common.Marshal(audits[0].Other.Op.Params)
			require.NoError(t, err)
			assert.JSONEq(t, `{"old_plan_id":0,"new_plan_id":`+planIdText+`}`, string(params))

			read := callSubscriptionSettings(t, AdminGetSubscriptionSettings, http.MethodGet, "")
			require.True(t, read.Success, read.Message)
			assert.Equal(t, plan.Id, read.Data.NewUserPlanId)

			for name, body := range map[string]string{
				"missing_field": `{}`,
				"negative":      `{"new_user_plan_id": -1}`,
				"not_a_number":  `{"new_user_plan_id": "x"}`,
			} {
				t.Run(name, func(t *testing.T) {
					rejected := callSubscriptionSettings(t, AdminUpdateSubscriptionSettings, http.MethodPut, body)
					assert.False(t, rejected.Success)
					assert.Equal(t, "Invalid parameters", rejected.Message)
					assert.Equal(t, plan.Id, operation_setting.GetSubscriptionSetting().NewUserPlanId)
					assert.Equal(t, planIdText, storedNewUserPlanOption(t))
				})
			}

			t.Run("unknown_plan", func(t *testing.T) {
				rejected := callSubscriptionSettings(t, AdminUpdateSubscriptionSettings, http.MethodPut, `{"new_user_plan_id": 999999}`)
				assert.False(t, rejected.Success)
				assert.Equal(t, "Subscription plan does not exist", rejected.Message)
				assert.Equal(t, plan.Id, operation_setting.GetSubscriptionSetting().NewUserPlanId)
			})

			t.Run("compliance_unconfirmed", func(t *testing.T) {
				operation_setting.GetPaymentSetting().ComplianceConfirmed = false
				t.Cleanup(func() { operation_setting.GetPaymentSetting().ComplianceConfirmed = true })
				rejected := callSubscriptionSettings(t, AdminUpdateSubscriptionSettings, http.MethodPut, `{"new_user_plan_id": 0}`)
				assert.False(t, rejected.Success)
				assert.Equal(t, plan.Id, operation_setting.GetSubscriptionSetting().NewUserPlanId)
			})

			t.Run("zero_turns_off", func(t *testing.T) {
				turnedOff := callSubscriptionSettings(t, AdminUpdateSubscriptionSettings, http.MethodPut, `{"new_user_plan_id": 0}`)
				require.True(t, turnedOff.Success, turnedOff.Message)
				assert.Equal(t, 0, operation_setting.GetSubscriptionSetting().NewUserPlanId)
				assert.Equal(t, "0", storedNewUserPlanOption(t))
			})

			// Runs last: it drops the options table.
			t.Run("database_write_failure_is_reported", func(t *testing.T) {
				require.NoError(t, model.DB.Migrator().DropTable(&model.Option{}))
				failed := callSubscriptionSettings(t, AdminUpdateSubscriptionSettings, http.MethodPut, `{"new_user_plan_id": `+planIdText+`}`)
				assert.False(t, failed.Success)
				assert.Equal(t, 0, operation_setting.GetSubscriptionSetting().NewUserPlanId)
			})
		})
	}
}

func TestNewUserDefaultSubscriptionGrant(t *testing.T) {
	for _, dialect := range newUserSubscriptionDialects {
		t.Run(dialect, func(t *testing.T) {
			setupNewUserSubscriptionTest(t, dialect)
			plan := seedNewUserPlan(t, "5h150M", "", newUserPlanQuota[dialect])

			assertGranted := func(t *testing.T, user *model.User) {
				t.Helper()
				subs := newUserSubscriptions(t, user.Id)
				require.Len(t, subs, 1)
				sub := subs[0]
				assert.Equal(t, plan.Id, sub.PlanId)
				assert.Equal(t, "new_user", sub.Source)
				assert.Equal(t, "active", sub.Status)
				assert.Equal(t, newUserPlanQuota[dialect], sub.AmountTotal)
				assert.Equal(t, int64(18000), sub.NextResetTime-sub.StartTime)
			}

			t.Run("insert_grants_plan", func(t *testing.T) {
				operation_setting.GetSubscriptionSetting().NewUserPlanId = plan.Id
				user := &model.User{Username: "nu-insert", Password: "password123", Role: common.RoleCommonUser}
				require.NoError(t, user.Insert(0))
				assertGranted(t, user)
			})

			t.Run("insert_with_tx_grants_plan", func(t *testing.T) {
				operation_setting.GetSubscriptionSetting().NewUserPlanId = plan.Id
				user := &model.User{Username: "nu-insert-tx", Role: common.RoleCommonUser}
				require.NoError(t, model.DB.Transaction(func(tx *gorm.DB) error {
					return user.InsertWithTx(tx, 0)
				}))
				assertGranted(t, user)
			})

			t.Run("outer_rollback_discards_grant", func(t *testing.T) {
				operation_setting.GetSubscriptionSetting().NewUserPlanId = plan.Id
				user := &model.User{Username: "nu-rollback", Role: common.RoleCommonUser}
				err := model.DB.Transaction(func(tx *gorm.DB) error {
					if err := user.InsertWithTx(tx, 0); err != nil {
						return err
					}
					return errors.New("later step in the same transaction failed")
				})
				require.Error(t, err)
				require.NotZero(t, user.Id)
				var users int64
				require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", "nu-rollback").Count(&users).Error)
				assert.Zero(t, users)
				assert.Empty(t, newUserSubscriptions(t, user.Id))
			})

			t.Run("disabled_setting_grants_nothing", func(t *testing.T) {
				operation_setting.GetSubscriptionSetting().NewUserPlanId = 0
				user := &model.User{Username: "nu-off", Password: "password123", Role: common.RoleCommonUser}
				require.NoError(t, user.Insert(0))
				assert.Empty(t, newUserSubscriptions(t, user.Id))
			})

			t.Run("missing_plan_skips_grant", func(t *testing.T) {
				operation_setting.GetSubscriptionSetting().NewUserPlanId = 999999
				user := &model.User{Username: "nu-missing", Password: "password123", Role: common.RoleCommonUser}
				require.NoError(t, user.Insert(0))
				var stored model.User
				require.NoError(t, model.DB.Where("username = ?", "nu-missing").First(&stored).Error)
				assert.Empty(t, newUserSubscriptions(t, stored.Id))
			})

			t.Run("compliance_unconfirmed_skips_grant", func(t *testing.T) {
				operation_setting.GetSubscriptionSetting().NewUserPlanId = plan.Id
				operation_setting.GetPaymentSetting().ComplianceConfirmed = false
				t.Cleanup(func() { operation_setting.GetPaymentSetting().ComplianceConfirmed = true })
				user := &model.User{Username: "nu-no-compliance", Password: "password123", Role: common.RoleCommonUser}
				require.NoError(t, user.Insert(0))
				assert.Empty(t, newUserSubscriptions(t, user.Id))
			})

			t.Run("upgrade_group", func(t *testing.T) {
				vipPlan := seedNewUserPlan(t, "vip", "vip", newUserPlanQuota[dialect])
				operation_setting.GetSubscriptionSetting().NewUserPlanId = vipPlan.Id
				user := &model.User{Username: "nu-vip", Password: "password123", Role: common.RoleCommonUser}
				require.NoError(t, user.Insert(0))
				assert.Equal(t, "vip", user.Group)
				var stored model.User
				require.NoError(t, model.DB.Where("id = ?", user.Id).First(&stored).Error)
				assert.Equal(t, "vip", stored.Group)
				subs := newUserSubscriptions(t, user.Id)
				require.Len(t, subs, 1)
				assert.Equal(t, "default", subs[0].PrevUserGroup)
			})
		})
	}
}
