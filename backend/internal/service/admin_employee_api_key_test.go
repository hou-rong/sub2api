//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func employeeProvisioningInput() AdminProvisionEmployeeAPIKeyInput {
	return AdminProvisionEmployeeAPIKeyInput{
		Email:       "hourong@zhihu.com",
		Concurrency: 5,
		APIKey: AdminEnsureAPIKeyInput{
			GroupID:       2,
			ExpiresInDays: 365,
		},
	}
}

func newEmployeeProvisioningService(
	userRepo UserRepository,
	group *Group,
	apiKeyRepo *apiKeyRepoStubForGroupUpdate,
) *adminServiceImpl {
	return &adminServiceImpl{
		userRepo:   userRepo,
		groupRepo:  &ensureGroupRepo{groupRepoStub: &groupRepoStub{}, group: group},
		apiKeyRepo: apiKeyRepo,
	}
}

func TestAdminProvisionEmployeeAPIKeyCreatesUserAndManagedKey(t *testing.T) {
	userRepo := &userRepoStub{nextID: 7}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{}
	svc := newEmployeeProvisioningService(
		userRepo,
		&Group{ID: 2, Status: StatusActive, IsExclusive: true},
		apiKeyRepo,
	)
	input := employeeProvisioningInput()
	input.Email = " Hourong@Zhihu.com "

	result, err := svc.ProvisionEmployeeAPIKey(context.Background(), input)
	require.NoError(t, err)
	require.True(t, result.UserCreated)
	require.True(t, result.APIKeyCreated)
	require.Equal(t, int64(7), result.User.ID)
	require.Equal(t, "hourong@zhihu.com", result.User.Email)
	require.Equal(t, "hourong", result.User.Username)
	require.Equal(t, "provisioned by zhishu", result.User.Notes)
	require.Equal(t, RoleUser, result.User.Role)
	require.Equal(t, StatusActive, result.User.Status)
	require.Equal(t, 5, result.User.Concurrency)
	require.Equal(t, []int64{2}, result.User.AllowedGroups)
	require.True(t, result.User.RestrictPublicGroups)
	require.NotEmpty(t, result.User.PasswordHash)
	require.Equal(t, "hourong-zhishu-client", result.APIKey.Name)
	require.True(t, result.APIKeyCreated)
	require.Len(t, userRepo.created, 1)
	require.NotNil(t, apiKeyRepo.ensureInput)
}

func TestAdminProvisionEmployeeAPIKeyReusesExistingUserAndKey(t *testing.T) {
	groupID := int64(2)
	user := &User{
		ID: 7, Email: "hourong@zhihu.com", Status: StatusActive,
		AllowedGroups: []int64{groupID}, RestrictPublicGroups: true,
	}
	userRepo := &userRepoStub{
		user:         user,
		usersByEmail: map[string]*User{user.Email: user},
	}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{
		ensureKey: &APIKey{
			ID: 11, UserID: user.ID, Key: "sk-existing", Name: "hourong-zhishu-client",
			GroupID: &groupID, Status: StatusActive,
		},
	}
	svc := newEmployeeProvisioningService(
		userRepo,
		&Group{ID: groupID, Status: StatusActive, IsExclusive: true},
		apiKeyRepo,
	)

	result, err := svc.ProvisionEmployeeAPIKey(context.Background(), employeeProvisioningInput())
	require.NoError(t, err)
	require.False(t, result.UserCreated)
	require.False(t, result.APIKeyCreated)
	require.Equal(t, "sk-existing", result.APIKey.Key)
	require.Empty(t, userRepo.created)
}

func TestAdminProvisionEmployeeAPIKeyCreatesKeyForExistingUser(t *testing.T) {
	user := &User{
		ID: 7, Email: "hourong@zhihu.com", Status: StatusActive,
		AllowedGroups: []int64{2}, RestrictPublicGroups: true,
	}
	userRepo := &userRepoStub{
		user:         user,
		usersByEmail: map[string]*User{user.Email: user},
	}
	svc := newEmployeeProvisioningService(
		userRepo,
		&Group{ID: 2, Status: StatusActive, IsExclusive: true},
		&apiKeyRepoStubForGroupUpdate{},
	)

	result, err := svc.ProvisionEmployeeAPIKey(context.Background(), employeeProvisioningInput())
	require.NoError(t, err)
	require.False(t, result.UserCreated)
	require.True(t, result.APIKeyCreated)
	require.Equal(t, "hourong-zhishu-client", result.APIKey.Name)
	require.Empty(t, userRepo.created)
}

func TestAdminProvisionEmployeeAPIKeyRecoversConcurrentEmailCreate(t *testing.T) {
	user := &User{
		ID: 7, Email: "hourong@zhihu.com", Status: StatusActive,
		AllowedGroups: []int64{2}, RestrictPublicGroups: true,
	}
	userRepo := &userRepoStub{
		user:             user,
		usersByEmail:     map[string]*User{user.Email: user},
		getByEmailMisses: 1,
		createErr:        ErrEmailExists,
	}
	svc := newEmployeeProvisioningService(
		userRepo,
		&Group{ID: 2, Status: StatusActive, IsExclusive: true},
		&apiKeyRepoStubForGroupUpdate{},
	)

	result, err := svc.ProvisionEmployeeAPIKey(context.Background(), employeeProvisioningInput())
	require.NoError(t, err)
	require.False(t, result.UserCreated)
	require.Equal(t, user.ID, result.User.ID)
	require.Empty(t, userRepo.created)
}

func TestAdminProvisionEmployeeAPIKeyRejectsInactiveUser(t *testing.T) {
	user := &User{ID: 7, Email: "hourong@zhihu.com", Status: StatusDisabled}
	userRepo := &userRepoStub{
		user:         user,
		usersByEmail: map[string]*User{user.Email: user},
	}
	svc := newEmployeeProvisioningService(
		userRepo,
		&Group{ID: 2, Status: StatusDisabled},
		&apiKeyRepoStubForGroupUpdate{},
	)

	_, err := svc.ProvisionEmployeeAPIKey(context.Background(), employeeProvisioningInput())
	require.Equal(t, 423, infraerrors.Code(err))
	require.Equal(t, "USER_INACTIVE", infraerrors.Reason(err))
}

func TestAdminProvisionEmployeeAPIKeyValidatesBeforeCreatingUser(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AdminProvisionEmployeeAPIKeyInput)
		group  *Group
	}{
		{
			name: "invalid email",
			mutate: func(input *AdminProvisionEmployeeAPIKeyInput) {
				input.Email = "not-an-email"
			},
			group: &Group{ID: 2, Status: StatusActive},
		},
		{
			name: "invalid expiry",
			mutate: func(input *AdminProvisionEmployeeAPIKeyInput) {
				input.APIKey.ExpiresInDays = 0
			},
			group: &Group{ID: 2, Status: StatusActive},
		},
		{
			name:   "inactive group",
			mutate: func(*AdminProvisionEmployeeAPIKeyInput) {},
			group:  &Group{ID: 2, Status: StatusDisabled},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			userRepo := &userRepoStub{nextID: 7}
			svc := newEmployeeProvisioningService(userRepo, test.group, &apiKeyRepoStubForGroupUpdate{})
			input := employeeProvisioningInput()
			test.mutate(&input)

			_, err := svc.ProvisionEmployeeAPIKey(context.Background(), input)
			require.Error(t, err)
			require.Empty(t, userRepo.created)
		})
	}
}
