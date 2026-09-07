//go:build unit

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProvisionEmployeeAPIKeyRequiresIdempotencyAndRejectsManagedFields(t *testing.T) {
	router, _ := setupAdminRouter()
	body := `{"email":"hourong@zhihu.com","group_id":2,"concurrency":5,"expires_in_days":365}`

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/provisioning/employee-api-key", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "IDEMPOTENCY_KEY_REQUIRED")
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "no-cache", recorder.Header().Get("Pragma"))

	for _, field := range []string{
		`"password":"caller-password"`,
		`"role":"admin"`,
		`"custom_key":"sk-caller-key"`,
	} {
		recorder = httptest.NewRecorder()
		managedBody := strings.TrimSuffix(body, "}") + "," + field + "}"
		request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/provisioning/employee-api-key", strings.NewReader(managedBody))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "managed-fields-test")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "managed by the server")
	}
}

func TestProvisionEmployeeAPIKeyCreatesAndReplaysWithoutPersistingCredential(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	repo := newMemoryIdempotencyRepoStub()
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })

	router, _ := setupAdminRouter()
	body := []byte(`{"email":"hourong@zhihu.com","group_id":2,"concurrency":5,"rpm_limit":0,"expires_in_days":365}`)
	request := func(payload []byte) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/provisioning/employee-api-key", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "zhishu-hourong-digest-v1")
		router.ServeHTTP(recorder, req)
		return recorder
	}

	first := request(body)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Empty(t, first.Header().Get("X-Idempotency-Replayed"))
	var firstPayload map[string]any
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstPayload))
	firstData, ok := firstPayload["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, firstData["user_created"])
	require.Equal(t, true, firstData["api_key_created"])
	user, ok := firstData["user"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "hourong@zhihu.com", user["email"])
	apiKey, ok := firstData["api_key"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "hourong-zhishu-client", apiKey["name"])
	credential, ok := apiKey["key"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(credential, "sk-"))

	replayed := request(body)
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
	require.Equal(t, "true", replayed.Header().Get("X-Idempotency-Replayed"))
	var replayedPayload map[string]any
	require.NoError(t, json.Unmarshal(replayed.Body.Bytes(), &replayedPayload))
	replayedData, ok := replayedPayload["data"].(map[string]any)
	require.True(t, ok)
	replayedAPIKey, ok := replayedData["api_key"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, credential, replayedAPIKey["key"])

	repo.mu.Lock()
	for _, record := range repo.data {
		if record.ResponseBody != nil {
			require.NotContains(t, *record.ResponseBody, credential)
			require.NotContains(t, *record.ResponseBody, "sk-")
			require.Contains(t, *record.ResponseBody, "user_id")
			require.Contains(t, *record.ResponseBody, "api_key_id")
		}
	}
	repo.mu.Unlock()

	conflictBody := []byte(`{"email":"hourong@zhihu.com","group_id":3,"concurrency":5,"expires_in_days":365}`)
	conflict := request(conflictBody)
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	require.Contains(t, conflict.Body.String(), "IDEMPOTENCY_KEY_CONFLICT")
}
