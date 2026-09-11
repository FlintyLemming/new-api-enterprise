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
