package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/langfuseconfig"

	"github.com/gin-gonic/gin"
)

// GetLangfuseSetting serves the dedicated read endpoint. All langfuse_setting.*
// keys are excluded from the generic options endpoint, so this is the only way
// to read the configuration and the secret never appears in either response.
func GetLangfuseSetting(c *gin.Context) {
	view, err := langfuseconfig.GetView()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    view,
	})
}

// UpdateLangfuseSetting saves the whole configuration in one transaction. A
// per-key generic update could leave a new host paired with an old key and
// break a working exporter, so the generic endpoint rejects these keys.
func UpdateLangfuseSetting(c *gin.Context) {
	var req langfuseconfig.UpdateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "无效的参数",
		})
		return
	}
	if err := langfuseconfig.Update(req); err != nil {
		if errors.Is(err, langfuseconfig.ErrStorage) {
			common.ApiError(c, err)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	// 只记录被修改的配置组名称，不记录任何配置值（含 Langfuse 密钥）。
	recordManageAudit(c, "option.update", map[string]interface{}{
		"key": "langfuse_setting",
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
}
