package controller

import (
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
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
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if nameLen := utf8.RuneCountInString(req.Name); nameLen < 1 || nameLen > 50 {
		common.ApiErrorT(c, "Reset card name length must be between 1-50")
		return
	}
	if req.Count < 1 || req.Count > 100 {
		common.ApiErrorT(c, "Grant count must be between 1-100")
		return
	}
	if req.ExpiredTime != 0 && req.ExpiredTime < common.GetTimestamp() {
		common.ApiErrorT(c, "Expiration time cannot be earlier than current time")
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
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	count, err := model.DisableSubscriptionResetCards(req.Ids)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrResetCardNotFound):
			common.ApiErrorT(c, "Subscription reset card not found")
		case errors.Is(err, model.ErrResetCardNotUnused):
			common.ApiErrorT(c, "Only unused reset cards can be disabled")
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
		common.ApiErrorT(c, "Invalid ID")
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
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	card, err := model.UseSubscriptionResetCard(userId, req.SubscriptionId)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrNoAvailableResetCard):
			common.ApiErrorT(c, "No available subscription reset card")
		case errors.Is(err, model.ErrResetCardAlreadyUsed):
			common.ApiErrorT(c, "This reset card has already been used")
		case errors.Is(err, model.ErrResetCardInvalidSubscription):
			common.ApiErrorT(c, "Subscription does not exist, does not belong to you, or is no longer active")
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
