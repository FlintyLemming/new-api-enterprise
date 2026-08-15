package langfuse

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

// Recorder holds everything one traced request owns: the runtime lease, the
// capture budget, the frozen input and the attempt value objects. The request
// path never creates an OTel span; spans are materialized in Finish or in the
// async worker with explicit historical timestamps (design §5).
type Recorder struct{}

// FromContext returns the Langfuse Recorder of the current request. A missing
// key, a mismatched type and a typed nil all yield nil, so every settlement
// function and attempt hook can call it without its own assertion.
func FromContext(c *gin.Context) *Recorder {
	if c == nil {
		return nil
	}
	value, ok := common.GetContextKey(c, constant.ContextKeyLangfuseRecorder)
	if !ok || value == nil {
		return nil
	}
	recorder, ok := value.(*Recorder)
	if !ok || recorder == nil {
		return nil
	}
	return recorder
}
