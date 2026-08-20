package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/exchange_key"
	"github.com/gin-gonic/gin"
)

func GetExchangeKeySetting(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    exchange_key.BuildView(),
	})
}

func UpdateExchangeKeySetting(c *gin.Context) {
	var req exchange_key.UpdateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "无效的参数",
		})
		return
	}

	candidate, err := exchange_key.ApplyUpdate(exchange_key.GetStored(), req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	if err := model.UpdateOptionsBulk(map[string]string{
		exchange_key.OptionKeyPrefix + "enabled": strconv.FormatBool(candidate.Enabled),
		exchange_key.OptionKeyPrefix + "secret":  candidate.Secret,
	}); err != nil {
		common.ApiError(c, err)
		return
	}

	recordManageAudit(c, "option.update", map[string]interface{}{
		"key": "exchange_key",
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
}
