package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsedChannelIDsFromContext(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set("use_channel", []string{"1", "abc", "2", "-3"})

	exclude := usedChannelIDsFromContext(c)
	require.Equal(t, map[int]bool{1: true, 2: true}, exclude)
}

func TestRetryParamResetRetryNextTry(t *testing.T) {
	param := &RetryParam{}
	param.ResetRetryNextTry()
	param.IncreaseRetry()
	assert.Equal(t, 0, param.GetRetry(), "reset flag must suppress the next increment")

	param.IncreaseRetry()
	assert.Equal(t, 1, param.GetRetry())
}
