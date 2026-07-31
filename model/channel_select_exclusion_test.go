package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupChannelSelectionCacheForTest(t *testing.T) {
	t.Helper()

	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
	})

	channelSyncLock.Lock()
	defer channelSyncLock.Unlock()

	channelsIDM = map[int]*Channel{
		1: {
			Id:       1,
			Type:     constant.ChannelTypeOpenAI,
			Status:   common.ChannelStatusEnabled,
			Name:     "low-priority",
			Weight:   lo.ToPtr(uint(10)),
			Priority: lo.ToPtr(int64(5)),
		},
		2: {
			Id:       2,
			Type:     constant.ChannelTypeOpenAI,
			Status:   common.ChannelStatusEnabled,
			Name:     "high-1",
			Weight:   lo.ToPtr(uint(10)),
			Priority: lo.ToPtr(int64(10)),
		},
		3: {
			Id:       3,
			Type:     constant.ChannelTypeOpenAI,
			Status:   common.ChannelStatusEnabled,
			Name:     "high-2",
			Weight:   lo.ToPtr(uint(10)),
			Priority: lo.ToPtr(int64(10)),
		},
	}
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-test": {1, 2, 3},
		},
	}
}

// Regression for #6571: after a channel-affinity hit selects a low-priority
// channel that fails, the retry must restart from the highest priority with the
// failed channel excluded, instead of re-selecting the same failed channel.
func TestGetRandomSatisfiedChannelExcludesFailedAffinityChannel(t *testing.T) {
	setupChannelSelectionCacheForTest(t)

	excludeAffinityChannel := map[int]bool{1: true}
	for i := 0; i < 20; i++ {
		ch, err := GetRandomSatisfiedChannel("default", "gpt-test", 0, "", excludeAffinityChannel)
		require.NoError(t, err)
		require.NotNil(t, ch)
		assert.NotEqual(t, 1, ch.Id, "failed affinity channel must never be re-selected")
		assert.EqualValues(t, 10, *ch.Priority, "retry must start from the highest priority")
	}
}

func TestGetRandomSatisfiedChannelExcludesAllCandidates(t *testing.T) {
	setupChannelSelectionCacheForTest(t)

	ch, err := GetRandomSatisfiedChannel("default", "gpt-test", 0, "", map[int]bool{1: true, 2: true, 3: true})
	require.NoError(t, err)
	assert.Nil(t, ch)
}

func TestGetRandomSatisfiedChannelExclusionAtLowerPriorityLevel(t *testing.T) {
	setupChannelSelectionCacheForTest(t)

	// retry=1 maps to the second-highest priority (5), which only contains the
	// already-failed channel. With it excluded, selection clamps to the only
	// remaining priority (10) and must pick one of the healthy channels.
	for i := 0; i < 20; i++ {
		ch, err := GetRandomSatisfiedChannel("default", "gpt-test", 1, "", map[int]bool{1: true})
		require.NoError(t, err)
		require.NotNil(t, ch)
		assert.NotEqual(t, 1, ch.Id)
		assert.EqualValues(t, 10, *ch.Priority)
	}
}
