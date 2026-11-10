package httpp

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestForLogQueryCredentials(t *testing.T) {
	r := httptest.NewRequest("GET", "http://localhost/camera/001?user=alice&pass=secret&token=jwt-value&other=keep", nil)
	logged := RequestForLog(r)
	for _, secret := range []string{"alice", "secret", "jwt-value"} {
		require.NotContains(t, logged, secret)
	}
	require.Contains(t, logged, "other=keep")
	require.Equal(t, "secret", r.URL.Query().Get("pass"))
}
