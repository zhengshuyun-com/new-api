package controller

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyVideoProxyResponseHeadersFiltersUpstreamCORS(t *testing.T) {
	// CORS headers are already written by the instance middleware before the
	// proxy handler runs; upstream values must not be appended on top of them.
	dst := http.Header{}
	dst.Set("Access-Control-Allow-Origin", "https://app.example.com")
	dst.Set("Access-Control-Allow-Credentials", "true")

	src := http.Header{}
	src.Set("Access-Control-Allow-Origin", "*")
	src.Add("Access-Control-Allow-Credentials", "true")
	src.Add("Access-Control-Expose-Headers", "X-Video-Info")
	src.Set("Content-Type", "video/mp4")
	src.Add("X-Custom", "a")
	src.Add("X-Custom", "b")

	copyVideoProxyResponseHeaders(dst, src)

	assert.Equal(t, []string{"https://app.example.com"}, dst.Values("Access-Control-Allow-Origin"))
	assert.Equal(t, []string{"true"}, dst.Values("Access-Control-Allow-Credentials"))
	assert.Empty(t, dst.Values("Access-Control-Expose-Headers"))
	assert.Equal(t, "video/mp4", dst.Get("Content-Type"))
	assert.Equal(t, []string{"a", "b"}, dst.Values("X-Custom"))
}

func TestCopyVideoProxyResponseHeadersPreservesMultiValueHeaders(t *testing.T) {
	dst := http.Header{}
	src := http.Header{}
	src.Add("X-Multi", "1")
	src.Add("X-Multi", "2")
	src.Set("ETag", `"abc"`)

	copyVideoProxyResponseHeaders(dst, src)

	require.Equal(t, []string{"1", "2"}, dst.Values("X-Multi"))
	assert.Equal(t, `"abc"`, dst.Get("ETag"))
}
