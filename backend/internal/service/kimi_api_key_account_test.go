//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kimi"
	"github.com/stretchr/testify/require"
)

func TestNormalizeKimiAPIKeyCredentialsDefaultsOfficialEndpoint(t *testing.T) {
	credentials := map[string]any{"api_key": "  kimi-secret  "}

	err := normalizeKimiAPIKeyCredentials(PlatformKimi, AccountTypeAPIKey, credentials)

	require.NoError(t, err)
	require.Equal(t, "kimi-secret", credentials["api_key"])
	require.Equal(t, kimi.DefaultBaseURL, credentials["base_url"])
}

func TestNormalizeKimiAPIKeyCredentialsRejectsMissingKey(t *testing.T) {
	err := normalizeKimiAPIKeyCredentials(PlatformKimi, AccountTypeAPIKey, map[string]any{})

	require.Error(t, err)
	require.Contains(t, err.Error(), "Kimi API Key is required")
}

func TestNormalizeKimiCodingCredentialsSupportsInternationalEndpoint(t *testing.T) {
	for _, baseURL := range []string{"https://api.kimi.ai/coding/v1", "https://api.kimi.ai:443/coding/v1/"} {
		t.Run(baseURL, func(t *testing.T) {
			credentials := map[string]any{"api_key": "kimi-secret", "account_mode": AccountModeCoding, "base_url": baseURL}
			require.NoError(t, normalizeKimiAPIKeyCredentials(PlatformKimi, AccountTypeAPIKey, credentials))
			account := &Account{Platform: PlatformKimi, Type: AccountTypeAPIKey, Credentials: credentials}
			require.True(t, account.UsesKimiCodingAPI())
			require.Equal(t, PlatformKimi, account.GetCodingPlanProvider())
			for suffix, build := range map[string]func(string) (string, error){
				"/chat/completions": kimi.BuildChatCompletionsURL,
				"/models":           kimi.BuildModelsURL,
				"/usages":           kimi.BuildUsagesURL,
			} {
				endpoint, err := build(account.GetKimiBaseURL())
				require.NoError(t, err)
				require.Equal(t, credentials["base_url"].(string)+suffix, endpoint)
			}
		})
	}
}

func TestNormalizeKimiCodingCredentialsRejectsSpoofedInternationalEndpoint(t *testing.T) {
	for _, baseURL := range []string{
		"https://api.kimi.ai.attacker.example/coding/v1",
		"https://api.kimi.ai@attacker.example/coding/v1",
		"https://attacker.example/api.kimi.ai/coding/v1",
	} {
		t.Run(baseURL, func(t *testing.T) {
			credentials := map[string]any{"api_key": "kimi-secret", "account_mode": AccountModeCoding, "base_url": baseURL}
			require.Error(t, normalizeKimiAPIKeyCredentials(PlatformKimi, AccountTypeAPIKey, credentials))
		})
	}
}

func TestNormalizeKimiAPIKeyCredentialsRejectsWrongKimiPlatformEndpoint(t *testing.T) {
	credentials := map[string]any{
		"api_key":      "kimi-secret",
		"base_url":     "https://api.moonshot.cn/v1",
		"account_mode": AccountModeCoding,
	}

	err := normalizeKimiAPIKeyCredentials(PlatformKimi, AccountTypeAPIKey, credentials)

	require.Error(t, err)
	require.Contains(t, err.Error(), "official Kimi Code")
}

func TestNormalizeKimiAPIKeyCredentialsAllowsPayGEndpoint(t *testing.T) {
	credentials := map[string]any{
		"api_key":      "  kimi-secret  ",
		"account_mode": AccountModePayG,
	}

	err := normalizeKimiAPIKeyCredentials(PlatformKimi, AccountTypeAPIKey, credentials)

	require.NoError(t, err)
	require.Equal(t, "kimi-secret", credentials["api_key"])
	require.Equal(t, DefaultKimiPayGBaseURL, credentials["base_url"])
}

func TestKimiAdaptiveAPIKeyDoesNotUseLegacyCodingTransport(t *testing.T) {
	account := &Account{
		Platform: PlatformKimi,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":      "kimi-secret",
			"api_protocol": APIProtocolAdaptive,
			"api_base_urls": map[string]any{
				APIProtocolChatCompletions: "https://api.moonshot.cn/v1",
				APIProtocolAnthropic:       "https://api.moonshot.cn/anthropic",
			},
		},
	}

	require.False(t, account.UsesKimiCodingAPI())
}

func TestKimiLegacyAPIKeyKeepsCodingTransport(t *testing.T) {
	account := &Account{
		Platform:    PlatformKimi,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "kimi-secret"},
	}

	require.True(t, account.UsesKimiCodingAPI())
}
