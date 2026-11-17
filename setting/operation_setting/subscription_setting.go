package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// SubscriptionSetting holds instance-wide subscription options.
type SubscriptionSetting struct {
	// NewUserPlanId is the plan granted to every newly created account; 0 turns it off.
	NewUserPlanId int `json:"new_user_plan_id"`
}

// SubscriptionNewUserPlanIdOptionKey is the options-table key of NewUserPlanId.
const SubscriptionNewUserPlanIdOptionKey = "subscription_setting.new_user_plan_id"

var subscriptionSetting = SubscriptionSetting{}

func init() {
	config.GlobalConfig.Register("subscription_setting", &subscriptionSetting)
}

func GetSubscriptionSetting() *SubscriptionSetting {
	return &subscriptionSetting
}
