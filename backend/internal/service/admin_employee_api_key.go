package service

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	adminProvisionedUserNotes               = "provisioned by zhishu"
	adminProvisionedUserPasswordRandomBytes = 32
	adminProvisionedAPIKeySuffix            = "-zhishu-client"
)

var ErrAdminEmployeeProvisioningUnavailable = infraerrors.ServiceUnavailable(
	"EMPLOYEE_API_KEY_PROVISIONING_UNAVAILABLE",
	"employee API key provisioning is unavailable",
)

// AdminProvisionEmployeeAPIKeyInput describes the administrator-only one-step
// employee credential workflow. Password, role, and allowed groups are derived
// by the service and cannot be supplied by callers.
type AdminProvisionEmployeeAPIKeyInput struct {
	Email        string
	Username     string
	KeyName      string
	Concurrency  int
	RPMLimit     int
	ActorAdminID int64
	APIKey       AdminEnsureAPIKeyInput
}

type AdminProvisionEmployeeAPIKeyResult struct {
	UserCreated   bool
	APIKeyCreated bool
	User          *User
	APIKey        *APIKey
}

func (s *adminServiceImpl) ProvisionEmployeeAPIKey(
	ctx context.Context,
	input AdminProvisionEmployeeAPIKeyInput,
) (*AdminProvisionEmployeeAPIKeyResult, error) {
	if s.userRepo == nil || s.groupRepo == nil || s.apiKeyRepo == nil {
		return nil, ErrAdminEmployeeProvisioningUnavailable
	}

	email, username, keyName, err := normalizeEmployeeProvisioningIdentity(input)
	if err != nil {
		return nil, err
	}
	input.APIKey.Name = keyName
	if _, err := validateAdminEnsureAPIKeyInput(input.APIKey); err != nil {
		return nil, err
	}
	if input.Concurrency <= 0 {
		return nil, infraerrors.BadRequest("INVALID_REQUEST", "concurrency must be greater than zero")
	}
	if input.RPMLimit < 0 {
		return nil, infraerrors.BadRequest("INVALID_REQUEST", "rpm_limit must be non-negative")
	}

	user, err := s.userRepo.GetByEmail(ctx, email)
	userMissing := errors.Is(err, ErrUserNotFound)
	if err != nil && !userMissing {
		return nil, err
	}
	if !userMissing {
		if user == nil || normalizeProvisioningEmail(user.Email) != email {
			return nil, ErrAdminEmployeeProvisioningUnavailable
		}
		if err := ValidateAdminManagedAPIKeyUserSnapshot(user); err != nil {
			return nil, err
		}
	}

	group, err := s.groupRepo.GetByID(ctx, input.APIKey.GroupID)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			return nil, ErrGroupNotAllowed
		}
		return nil, err
	}
	if group == nil || !group.IsActive() {
		return nil, ErrGroupNotAllowed
	}

	userCreated := false
	if userMissing {
		password, passwordErr := randomHexString(adminProvisionedUserPasswordRandomBytes)
		if passwordErr != nil {
			return nil, ErrAdminEmployeeProvisioningUnavailable.WithCause(passwordErr)
		}
		user, err = s.CreateUser(ctx, &CreateUserInput{
			Email:                email,
			Password:             password,
			Username:             username,
			Notes:                adminProvisionedUserNotes,
			Role:                 RoleUser,
			Concurrency:          input.Concurrency,
			RPMLimit:             input.RPMLimit,
			AllowedGroups:        []int64{input.APIKey.GroupID},
			RestrictPublicGroups: true,
			ActorAdminID:         input.ActorAdminID,
		})
		if errors.Is(err, ErrEmailExists) {
			user, err = s.userRepo.GetByEmail(ctx, email)
		} else if err == nil {
			userCreated = true
		}
	}
	if err != nil {
		return nil, err
	}
	if user == nil || normalizeProvisioningEmail(user.Email) != email {
		return nil, ErrAdminEmployeeProvisioningUnavailable
	}
	if err := ValidateAdminManagedAPIKeyUserSnapshot(user); err != nil {
		return nil, err
	}

	ensured, err := s.EnsureUserAPIKey(ctx, user.ID, input.APIKey)
	if err != nil {
		return nil, err
	}
	if ensured == nil || ensured.APIKey == nil {
		return nil, ErrAdminEmployeeProvisioningUnavailable
	}
	return &AdminProvisionEmployeeAPIKeyResult{
		UserCreated:   userCreated,
		APIKeyCreated: ensured.Created,
		User:          user,
		APIKey:        ensured.APIKey,
	}, nil
}

func normalizeEmployeeProvisioningIdentity(input AdminProvisionEmployeeAPIKeyInput) (string, string, string, error) {
	email := normalizeProvisioningEmail(input.Email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Name != "" || !strings.EqualFold(address.Address, email) || len(email) > 255 {
		return "", "", "", infraerrors.BadRequest("INVALID_REQUEST", "email must be a valid address")
	}
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return "", "", "", infraerrors.BadRequest("INVALID_REQUEST", "email must be a valid address")
	}

	username := strings.TrimSpace(input.Username)
	if username == "" {
		username = email[:at]
	}
	if utf8.RuneCountInString(username) > 100 {
		return "", "", "", infraerrors.BadRequest("INVALID_REQUEST", "username must not exceed 100 characters")
	}

	keyName := strings.TrimSpace(input.KeyName)
	if keyName == "" {
		keyName = email[:at] + adminProvisionedAPIKeySuffix
	}
	return email, username, keyName, nil
}

func normalizeProvisioningEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
