package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The fork's account Fast switch runs before the upstream group policy. A
// supported, explicitly forced Ultra Fast group must win in both transports.
func TestGroupUltrafastOverridesAccountFastMode(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "account_fast_disabled"
		if enabled {
			name = "account_fast_enabled"
		}
		t.Run(name, func(t *testing.T) {
			svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
			account := &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{OpenAIFastModeEnabledExtraKey: enabled},
			}
			body := []byte(`{"model":"gpt-6.1-sol","service_tier":"priority","input":"hi"}`)
			updated, err := svc.applyOpenAIFastPolicyToBody(openAIUltrafastGroupContext(), account, "gpt-6.1-sol", body)
			require.NoError(t, err)
			require.Equal(t, OpenAIFastTierUltrafast, gjson.GetBytes(updated, "service_tier").String())

			frame := []byte(`{"type":"response.create","model":"gpt-6.1-sol","service_tier":"priority","input":"hi"}`)
			updated, blocked, err := svc.applyOpenAIFastPolicyToWSResponseCreate(openAIUltrafastGroupContext(), account, "gpt-6.1-sol", frame)
			require.NoError(t, err)
			require.Nil(t, blocked)
			require.Equal(t, OpenAIFastTierUltrafast, gjson.GetBytes(updated, "service_tier").String())
		})
	}
}
