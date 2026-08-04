package service

import (
	"context"
	"strings"
	"testing"
)

type opsWeComNotifierStub struct {
	content string
	url     string
	err     error
}

func (s *opsWeComNotifierStub) SendMarkdown(_ context.Context, webhookURL, content string) error {
	s.url = webhookURL
	s.content = content
	return s.err
}

func TestValidateOpsWeComWebhookURL(t *testing.T) {
	valid := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=1234567890abcdef"
	if err := validateOpsWeComWebhookURL(valid); err != nil {
		t.Fatalf("valid URL rejected: %v", err)
	}
	for _, raw := range []string{
		"http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x",
		"https://example.com/cgi-bin/webhook/send?key=x",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send",
		"https://qyapi.weixin.qq.com.evil.test/cgi-bin/webhook/send?key=x",
	} {
		if err := validateOpsWeComWebhookURL(raw); err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
}

func TestOpsWeComConfigMasksWebhookAndPreservesSecret(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}
	webhook := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=1234567890abcdef"

	view, err := svc.UpdateWeComNotificationConfig(context.Background(), &OpsWeComNotificationConfigUpdateRequest{
		Enabled:          true,
		WebhookURL:       &webhook,
		MinSeverity:      "P1",
		RateLimitPerHour: 20,
	})
	if err != nil {
		t.Fatalf("UpdateWeComNotificationConfig() error = %v", err)
	}
	if !view.WebhookConfigured || strings.Contains(view.WebhookURLMasked, "1234567890abcdef") {
		t.Fatalf("webhook was not masked: %#v", view)
	}
	if strings.Contains(view.WebhookURLMasked, "webhook/send?key=1234") {
		t.Fatalf("webhook prefix leaked: %s", view.WebhookURLMasked)
	}

	loaded, err := svc.GetWeComNotificationConfig(context.Background())
	if err != nil {
		t.Fatalf("GetWeComNotificationConfig() error = %v", err)
	}
	if loaded.WebhookURL != webhook {
		t.Fatalf("stored webhook changed: %q", loaded.WebhookURL)
	}

	view, err = svc.UpdateWeComNotificationConfig(context.Background(), &OpsWeComNotificationConfigUpdateRequest{
		Enabled:          true,
		MinSeverity:      "P2",
		RateLimitPerHour: 10,
	})
	if err != nil {
		t.Fatalf("partial update error = %v", err)
	}
	if !view.WebhookConfigured {
		t.Fatal("partial update cleared webhook")
	}
}

func TestOpsWeComTestMessageUsesStoredWebhook(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	webhook := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=1234567890abcdef"
	notifier := &opsWeComNotifierStub{}
	svc := &OpsService{settingRepo: repo, weComNotifier: notifier}
	if _, err := svc.UpdateWeComNotificationConfig(context.Background(), &OpsWeComNotificationConfigUpdateRequest{
		WebhookURL: &webhook,
	}); err != nil {
		t.Fatalf("store config: %v", err)
	}
	if err := svc.TestWeComNotification(context.Background()); err != nil {
		t.Fatalf("TestWeComNotification() error = %v", err)
	}
	if notifier.url != webhook || !strings.Contains(notifier.content, "通知测试") {
		t.Fatalf("unexpected test delivery: %#v", notifier)
	}
}

func TestShouldSendOpsAlertWeComByMinSeverity(t *testing.T) {
	if !shouldSendOpsAlertWeComByMinSeverity("P1", "P0") {
		t.Fatal("P0 should pass a P1 threshold")
	}
	if shouldSendOpsAlertWeComByMinSeverity("P1", "P2") {
		t.Fatal("P2 should not pass a P1 threshold")
	}
	if !shouldSendOpsAlertWeComByMinSeverity("", "P3") {
		t.Fatal("empty threshold should allow all severities")
	}
}

func TestBuildOpsAlertWeComMarkdownUsesSiteNameAndStatusEmoji(t *testing.T) {
	tests := []struct {
		name     string
		siteName string
		status   string
		want     string
	}{
		{
			name:     "triggered alert uses configured site name",
			siteName: "知枢token供应商",
			status:   OpsAlertStatusFiring,
			want:     "## 🔥知枢token供应商运维预警触发\n",
		},
		{
			name:     "resolved alert uses configured site name",
			siteName: "知枢token供应商",
			status:   OpsAlertStatusResolved,
			want:     "## ✅知枢token供应商运维预警恢复\n",
		},
		{
			name:     "empty site name falls back to Sub2API",
			siteName: "  ",
			status:   OpsAlertStatusManualResolved,
			want:     "## ✅Sub2API运维预警恢复\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := buildOpsAlertWeComMarkdown(tt.siteName, &OpsAlertRule{
				Name:     "可用账号不足",
				Severity: "P1",
			}, &OpsAlertEvent{
				Status:      tt.status,
				Description: "当前无可用账号",
			})
			if !strings.HasPrefix(content, tt.want) {
				t.Fatalf("unexpected title: %q", content)
			}
		})
	}
}

func TestOpsAlertSiteNameUsesSettingWithFallback(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsAlertEvaluatorService{opsService: &OpsService{settingRepo: repo}}

	if got := svc.opsAlertSiteName(context.Background()); got != defaultOpsAlertSiteName {
		t.Fatalf("missing site name = %q, want %q", got, defaultOpsAlertSiteName)
	}
	repo.values[SettingKeySiteName] = "  知枢token供应商  "
	if got := svc.opsAlertSiteName(context.Background()); got != "知枢token供应商" {
		t.Fatalf("configured site name = %q", got)
	}
	repo.values[SettingKeySiteName] = "  "
	if got := svc.opsAlertSiteName(context.Background()); got != defaultOpsAlertSiteName {
		t.Fatalf("blank site name = %q, want %q", got, defaultOpsAlertSiteName)
	}
}
