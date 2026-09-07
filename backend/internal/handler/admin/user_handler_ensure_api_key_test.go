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

func TestEnsureUserAPIKeyRequiresIdempotencyKeyAndRejectsCustomKey(t *testing.T) {
	router, _ := setupAdminRouter()
	body := `{"name":"alice-zhishu-client","group_id":2,"expires_in_days":365}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/1/api-keys/ensure", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "IDEMPOTENCY_KEY_REQUIRED")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "no-cache", rec.Header().Get("Pragma"))

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/1/api-keys/ensure", strings.NewReader(
		`{"name":"alice-zhishu-client","group_id":2,"expires_in_days":365,"custom_key":"sk-forbidden"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-custom-key")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "custom_key is not accepted")
}

func TestEnsureUserAPIKeyReplaysCreatedResultWithoutPersistingCredential(t *testing.T) {
	previous := service.DefaultIdempotencyCoordinator()
	repo := newMemoryIdempotencyRepoStub()
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })

	router, adminService := setupAdminRouter()
	body := []byte(`{"name":"alice-zhishu-client","group_id":2,"expires_in_days":365}`)
	request := func(groupID int) *httptest.ResponseRecorder {
		payload := body
		if groupID != 2 {
			payload = []byte(`{"name":"alice-zhishu-client","group_id":3,"expires_in_days":365}`)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/1/api-keys/ensure", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "zhishu-test-alice-v1")
		router.ServeHTTP(rec, req)
		return rec
	}

	first := request(2)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Empty(t, first.Header().Get("X-Idempotency-Replayed"))
	var firstPayload map[string]any
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstPayload))
	firstData, ok := firstPayload["data"].(map[string]any)
	require.True(t, ok, "response data must be an object")
	require.Equal(t, true, firstData["created"])
	firstAPIKey, ok := firstData["api_key"].(map[string]any)
	require.True(t, ok, "response api_key must be an object")
	firstKey, ok := firstAPIKey["key"].(string)
	require.True(t, ok, "response api_key.key must be a string")
	require.True(t, strings.HasPrefix(firstKey, "sk-"))

	replayed := request(2)
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
	require.Equal(t, "true", replayed.Header().Get("X-Idempotency-Replayed"))
	var replayedPayload map[string]any
	require.NoError(t, json.Unmarshal(replayed.Body.Bytes(), &replayedPayload))
	replayedData, ok := replayedPayload["data"].(map[string]any)
	require.True(t, ok, "replayed response data must be an object")
	require.Equal(t, true, replayedData["created"])
	replayedAPIKey, ok := replayedData["api_key"].(map[string]any)
	require.True(t, ok, "replayed response api_key must be an object")
	replayedKey, ok := replayedAPIKey["key"].(string)
	require.True(t, ok, "replayed response api_key.key must be a string")
	require.Equal(t, firstKey, replayedKey)

	repo.mu.Lock()
	for _, record := range repo.data {
		if record.ResponseBody != nil {
			require.NotContains(t, *record.ResponseBody, "sk-")
			require.NotContains(t, *record.ResponseBody, firstKey)
			require.Contains(t, *record.ResponseBody, "api_key_id")
		}
	}
	repo.mu.Unlock()

	conflict := request(3)
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	require.Contains(t, conflict.Body.String(), "IDEMPOTENCY_KEY_CONFLICT")

	for i := range adminService.apiKeys {
		if adminService.apiKeys[i].Name == "alice-zhishu-client" {
			adminService.apiKeys[i].Status = service.StatusDisabled
		}
	}
	unusableReplay := request(2)
	require.Equal(t, http.StatusConflict, unusableReplay.Code, unusableReplay.Body.String())
	require.Contains(t, unusableReplay.Body.String(), "API_KEY_UNUSABLE")

	for i := range adminService.apiKeys {
		if adminService.apiKeys[i].Name == "alice-zhishu-client" {
			adminService.apiKeys[i].Status = service.StatusActive
		}
	}
	adminService.users[0].Status = service.StatusDisabled
	inactiveUserReplay := request(2)
	require.Equal(t, http.StatusLocked, inactiveUserReplay.Code, inactiveUserReplay.Body.String())
	require.Contains(t, inactiveUserReplay.Body.String(), "USER_INACTIVE")

	adminService.users[0].Status = service.StatusActive
	adminService.groups[0].Status = service.StatusDisabled
	inactiveGroupReplay := request(2)
	require.Equal(t, http.StatusForbidden, inactiveGroupReplay.Code, inactiveGroupReplay.Body.String())
	require.Contains(t, inactiveGroupReplay.Body.String(), "GROUP_NOT_ALLOWED")
}
