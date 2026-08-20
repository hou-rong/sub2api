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
