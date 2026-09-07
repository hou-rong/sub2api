package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"html"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
)

const (
	adminManagedAPIKeyPrefix       = "sk-"
	adminManagedAPIKeyRandomBytes  = 32
	adminManagedAPIKeyCreateTries  = 3
	adminManagedAPIKeyMaxExpiryDay = 36500
)

var (
	ErrAPIKeyNameConflict = infraerrors.Conflict(
		"API_KEY_NAME_CONFLICT",
		"multiple API keys already use this name for the user",
	)
	ErrAPIKeyConfigurationConflict = infraerrors.Conflict(
		"API_KEY_CONFIGURATION_CONFLICT",
		"the existing API key is bound to a different group",
	)
	ErrAPIKeyUnusable = infraerrors.Conflict(
		"API_KEY_UNUSABLE",
		"the existing API key is disabled, expired, or exhausted",
	)
	ErrAdminManagedAPIKeyEnsureUnavailable = infraerrors.ServiceUnavailable(
		"API_KEY_ENSURE_UNAVAILABLE",
		"managed API key provisioning is unavailable",
	)
	ErrAdminManagedAPIKeyReplayUnavailable = infraerrors.ServiceUnavailable(
		"API_KEY_REPLAY_UNAVAILABLE",
		"the ensured API key can no longer be resolved",
	)
	ErrAdminManagedUserInactive = infraerrors.New(
		http.StatusLocked,
		"USER_INACTIVE",
		"user account is disabled",
	)
)

// AdminEnsureAPIKeyInput is the administrator-only, server-generated-key
// contract used by trusted provisioning services.
type AdminEnsureAPIKeyInput struct {
	Name          string
	GroupID       int64
	Quota         float64
	ExpiresInDays int
	RateLimit5h   float64
	RateLimit1d   float64
	RateLimit7d   float64
	IPWhitelist   []string
	IPBlacklist   []string
}

type AdminEnsureAPIKeyResult struct {
	Created bool
	APIKey  *APIKey
}

type adminManagedAPIKeyRepository interface {
	EnsureByUserIDAndName(
		ctx context.Context,
		candidate *APIKey,
	) (apiKey *APIKey, created bool, err error)
}

func (s *adminServiceImpl) EnsureUserAPIKey(
	ctx context.Context,
	userID int64,
	input AdminEnsureAPIKeyInput,
) (*AdminEnsureAPIKeyResult, error) {
	if userID <= 0 {
		return nil, ErrUserNotFound
	}
	if s.userRepo == nil || s.groupRepo == nil {
		return nil, ErrAdminManagedAPIKeyEnsureUnavailable
	}

	normalizedName, err := validateAdminEnsureAPIKeyInput(input)
	if err != nil {
		return nil, err
	}

	if err := s.ValidateUserAPIKeyProvisioningAccess(ctx, userID, input.GroupID); err != nil {
		return nil, err
	}

	ensureRepo, ok := s.apiKeyRepo.(adminManagedAPIKeyRepository)
	if !ok || ensureRepo == nil {
		return nil, ErrAdminManagedAPIKeyEnsureUnavailable
	}

	for attempt := 0; attempt < adminManagedAPIKeyCreateTries; attempt++ {
		key, err := generateAdminManagedAPIKey()
		if err != nil {
			return nil, ErrAdminManagedAPIKeyEnsureUnavailable.WithCause(err)
		}
		expiresAt := time.Now().UTC().AddDate(0, 0, input.ExpiresInDays)
		groupID := input.GroupID
		candidate := &APIKey{
			UserID:      userID,
			Key:         key,
			Name:        normalizedName,
			GroupID:     &groupID,
			Status:      StatusActive,
			IPWhitelist: append([]string(nil), input.IPWhitelist...),
			IPBlacklist: append([]string(nil), input.IPBlacklist...),
			Quota:       input.Quota,
			ExpiresAt:   &expiresAt,
			RateLimit5h: input.RateLimit5h,
			RateLimit1d: input.RateLimit1d,
			RateLimit7d: input.RateLimit7d,
		}

		ensured, created, ensureErr := ensureRepo.EnsureByUserIDAndName(ctx, candidate)
		if ensureErr != nil {
			if errors.Is(ensureErr, ErrAPIKeyExists) {
				continue
			}
			return nil, ensureErr
		}
		if err := ValidateAdminManagedAPIKeyForEnsure(ensured, userID, input); err != nil {
			return nil, err
		}
		if created && s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByKey(ctx, ensured.Key)
		}
		return &AdminEnsureAPIKeyResult{Created: created, APIKey: ensured}, nil
	}

	return nil, ErrAdminManagedAPIKeyEnsureUnavailable
}

func validateAdminEnsureAPIKeyInput(input AdminEnsureAPIKeyInput) (string, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", infraerrors.BadRequest("INVALID_REQUEST", "name is required and must not exceed 100 characters")
	}
	normalizedName := html.EscapeString(name)
	if utf8.RuneCountInString(normalizedName) > 100 {
		return "", infraerrors.BadRequest("INVALID_REQUEST", "escaped name must not exceed 100 characters")
	}
	if input.GroupID <= 0 {
		return "", infraerrors.BadRequest("INVALID_REQUEST", "group_id must be greater than zero")
	}
	if input.ExpiresInDays <= 0 || input.ExpiresInDays > adminManagedAPIKeyMaxExpiryDay {
		return "", infraerrors.BadRequest("INVALID_REQUEST", "expires_in_days must be between 1 and 36500")
	}
	for _, value := range []float64{input.Quota, input.RateLimit5h, input.RateLimit1d, input.RateLimit7d} {
		if err := validateAPIKeyLimit(value); err != nil {
			return "", infraerrors.BadRequest("INVALID_REQUEST", "numeric limits must be finite and non-negative")
		}
	}
	if invalid := ip.ValidateIPPatterns(input.IPWhitelist); len(invalid) > 0 {
		return "", infraerrors.BadRequest("INVALID_REQUEST", "ip_whitelist contains an invalid IP or CIDR")
	}
	if invalid := ip.ValidateIPPatterns(input.IPBlacklist); len(invalid) > 0 {
		return "", infraerrors.BadRequest("INVALID_REQUEST", "ip_blacklist contains an invalid IP or CIDR")
	}
	return normalizedName, nil
}

// ValidateUserAPIKeyProvisioningAccess checks the mutable user, group, and
// subscription authorization without creating or changing an API key.
func (s *adminServiceImpl) ValidateUserAPIKeyProvisioningAccess(ctx context.Context, userID, groupID int64) error {
	if userID <= 0 {
		return ErrUserNotFound
	}
	if groupID <= 0 {
		return infraerrors.BadRequest("INVALID_REQUEST", "group_id must be greater than zero")
	}
	if s.userRepo == nil || s.groupRepo == nil {
		return ErrAdminManagedAPIKeyEnsureUnavailable
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := ValidateAdminManagedAPIKeyUserSnapshot(user); err != nil {
		return err
	}
	group, err := s.groupRepo.GetByID(ctx, groupID)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			return ErrGroupNotAllowed
		}
		return err
	}
	subscriptionActive := false
	if group != nil && group.IsSubscriptionType() {
		if s.userSubRepo == nil {
			return infraerrors.InternalServer(
				"SUBSCRIPTION_REPOSITORY_UNAVAILABLE",
				"subscription repository is not configured",
			)
		}
		if _, err := s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, user.ID, group.ID); err != nil {
			if !errors.Is(err, ErrSubscriptionNotFound) {
				return err
			}
		} else {
			subscriptionActive = true
		}
	}
	return ValidateAdminManagedAPIKeyProvisioningSnapshot(user, group, subscriptionActive)
}

// ValidateAdminManagedAPIKeyProvisioningSnapshot is the single authorization
// rule used by both the service precheck and the repository's locked snapshot.
func ValidateAdminManagedAPIKeyProvisioningSnapshot(user *User, group *Group, subscriptionActive bool) error {
	if err := ValidateAdminManagedAPIKeyUserSnapshot(user); err != nil {
		return err
	}
	if group == nil || group.ID <= 0 || !group.IsActive() {
		return ErrGroupNotAllowed
	}
	if group.IsSubscriptionType() {
		if !subscriptionActive {
			return ErrGroupNotAllowed
		}
		return nil
	}
	if !user.CanBindGroup(group.ID, group.IsExclusive) {
		return ErrGroupNotAllowed
	}
	return nil
}

// ValidateAdminManagedAPIKeyUserSnapshot preserves the USER_INACTIVE error
// priority as soon as a trusted user snapshot has been read or locked.
func ValidateAdminManagedAPIKeyUserSnapshot(user *User) error {
	if user == nil || user.ID <= 0 {
		return ErrUserNotFound
	}
	if !user.IsActive() {
		return ErrAdminManagedUserInactive
	}
	return nil
}

// ValidateAdminManagedAPIKeyForEnsure revalidates a resolved key before it is
// returned, including after an idempotency replay.
func ValidateAdminManagedAPIKeyForEnsure(apiKey *APIKey, userID int64, input AdminEnsureAPIKeyInput) error {
	expectedName := html.EscapeString(strings.TrimSpace(input.Name))
	if apiKey == nil || apiKey.ID <= 0 || apiKey.UserID != userID || apiKey.Name != expectedName ||
		!strings.HasPrefix(apiKey.Key, adminManagedAPIKeyPrefix) || len(apiKey.Key) <= len(adminManagedAPIKeyPrefix) {
		return ErrAdminManagedAPIKeyReplayUnavailable
	}
	if apiKey.GroupID == nil || *apiKey.GroupID != input.GroupID {
		return ErrAPIKeyConfigurationConflict
	}
	if !apiKey.IsActive() || apiKey.IsExpired() || apiKey.IsQuotaExhausted() {
		return ErrAPIKeyUnusable
	}
	return nil
}

func generateAdminManagedAPIKey() (string, error) {
	random := make([]byte, adminManagedAPIKeyRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return adminManagedAPIKeyPrefix + hex.EncodeToString(random), nil
}
