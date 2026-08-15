package langfuse

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NotNil(t, c)
	return c
}

func TestFromContextIsNilSafe(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		assert.Nil(t, FromContext(nil))
	})

	t.Run("key absent", func(t *testing.T) {
		assert.Nil(t, FromContext(newTestContext(t)))
	})

	t.Run("typed nil recorder", func(t *testing.T) {
		c := newTestContext(t)
		common.SetContextKey(c, constant.ContextKeyLangfuseRecorder, (*Recorder)(nil))
		assert.Nil(t, FromContext(c))
	})

	t.Run("untyped nil", func(t *testing.T) {
		c := newTestContext(t)
		common.SetContextKey(c, constant.ContextKeyLangfuseRecorder, nil)
		assert.Nil(t, FromContext(c))
	})

	t.Run("wrong type", func(t *testing.T) {
		c := newTestContext(t)
		common.SetContextKey(c, constant.ContextKeyLangfuseRecorder, "wrong")
		assert.Nil(t, FromContext(c))
	})

	t.Run("stored recorder round trips", func(t *testing.T) {
		c := newTestContext(t)
		recorder := &Recorder{}
		common.SetContextKey(c, constant.ContextKeyLangfuseRecorder, recorder)
		assert.Same(t, recorder, FromContext(c))
	})
}
