//go:build unit

package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type ensureGroupRepo struct {
	*groupRepoStub
	group *Group
	err   error
}

func (r *ensureGroupRepo) GetByID(context.Context, int64) (*Group, error) {
	return r.group, r.err
}

func newManagedAPIKeyServiceForTest(user *User, group *Group, repo *apiKeyRepoStubForGroupUpdate) *adminServiceImpl {
	return &adminServiceImpl{
		userRepo:   &userRepoStub{user: user},
		groupRepo:  &ensureGroupRepo{groupRepoStub: &groupRepoStub{}, group: group},
		apiKeyRepo: repo,
	}
}

func managedAPIKeyInput() AdminEnsureAPIKeyInput {
	return AdminEnsureAPIKeyInput{
		Name:          "alice-zhishu-client",
		GroupID:       2,
		ExpiresInDays: 365,
	}
}

func TestAdminEnsureUserAPIKeyCreatesServerGeneratedCredential(t *testing.T) {
	user := &User{ID: 7, Status: StatusActive, AllowedGroups: []int64{2}}
	group := &Group{ID: 2, Status: StatusActive, IsExclusive: true}
	repo := &apiKeyRepoStubForGroupUpdate{}
	svc := newManagedAPIKeyServiceForTest(user, group, repo)

	result, err := svc.EnsureUserAPIKey(context.Background(), user.ID, managedAPIKeyInput())
	require.NoError(t, err)
	require.True(t, result.Created)
	require.NotNil(t, result.APIKey)
	require.Equal(t, "alice-zhishu-client", result.APIKey.Name)
	require.Len(t, result.APIKey.Key, len("sk-")+64)
	require.Contains(t, result.APIKey.Key, "sk-")
	require.NotNil(t, repo.ensureInput)
	require.WithinDuration(t, time.Now().UTC().AddDate(0, 0, 365), *repo.ensureInput.ExpiresAt, 2*time.Second)
}

func TestAdminEnsureUserAPIKeyRejectsInactiveUserAndUnusableExistingKey(t *testing.T) {
	group := &Group{ID: 2, Status: StatusActive}
	repo := &apiKeyRepoStubForGroupUpdate{}
	svc := newManagedAPIKeyServiceForTest(&User{ID: 7, Status: StatusDisabled}, group, repo)

	_, err := svc.EnsureUserAPIKey(context.Background(), 7, managedAPIKeyInput())
	require.Equal(t, 423, infraerrors.Code(err))
	require.Equal(t, "USER_INACTIVE", infraerrors.Reason(err))

	groupID := int64(2)
	repo.ensureKey = &APIKey{
		ID: 8, UserID: 7, Key: "sk-existing", Name: "alice-zhishu-client",
		GroupID: &groupID, Status: StatusDisabled,
	}
	svc = newManagedAPIKeyServiceForTest(&User{ID: 7, Status: StatusActive}, group, repo)
	_, err = svc.EnsureUserAPIKey(context.Background(), 7, managedAPIKeyInput())
	require.ErrorIs(t, err, ErrAPIKeyUnusable)
}

func TestAdminEnsureUserAPIKeyKeepsInactiveUserErrorPriority(t *testing.T) {
	user := &User{ID: 7, Status: StatusDisabled}
	repo := &apiKeyRepoStubForGroupUpdate{}
	svc := newManagedAPIKeyServiceForTest(user, nil, repo)
	svc.groupRepo = &ensureGroupRepo{
		groupRepoStub: &groupRepoStub{},
		err:           ErrGroupNotFound,
	}

	_, err := svc.EnsureUserAPIKey(context.Background(), user.ID, managedAPIKeyInput())
	require.ErrorIs(t, err, ErrAdminManagedUserInactive)
	require.Nil(t, repo.ensureInput)
}

func TestAdminEnsureUserAPIKeyRejectsInvalidLimitsAndUnauthorizedGroup(t *testing.T) {
	user := &User{ID: 7, Status: StatusActive}
	group := &Group{ID: 2, Status: StatusActive, IsExclusive: true}
	svc := newManagedAPIKeyServiceForTest(user, group, &apiKeyRepoStubForGroupUpdate{})

	input := managedAPIKeyInput()
	input.Quota = math.Inf(1)
	_, err := svc.EnsureUserAPIKey(context.Background(), user.ID, input)
	require.Equal(t, "INVALID_REQUEST", infraerrors.Reason(err))

	input = managedAPIKeyInput()
	_, err = svc.EnsureUserAPIKey(context.Background(), user.ID, input)
	require.ErrorIs(t, err, ErrGroupNotAllowed)

	input = managedAPIKeyInput()
	input.Name = strings.Repeat("<", 100)
	_, err = svc.EnsureUserAPIKey(context.Background(), user.ID, input)
	require.Equal(t, "INVALID_REQUEST", infraerrors.Reason(err))
}

func TestAdminEnsureUserAPIKeyPreservesSubscriptionRepositoryFailure(t *testing.T) {
	user := &User{ID: 7, Status: StatusActive}
	group := &Group{ID: 2, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription}
	repo := &apiKeyRepoStubForGroupUpdate{}
	svc := newManagedAPIKeyServiceForTest(user, group, repo)

	upstreamErr := errors.New("subscription repository unavailable")
	svc.userSubRepo = &userSubRepoStubForGroupUpdate{getActiveErr: upstreamErr}
	_, err := svc.EnsureUserAPIKey(context.Background(), user.ID, managedAPIKeyInput())
	require.ErrorIs(t, err, upstreamErr)

	svc.userSubRepo = &userSubRepoStubForGroupUpdate{getActiveErr: ErrSubscriptionNotFound}
	_, err = svc.EnsureUserAPIKey(context.Background(), user.ID, managedAPIKeyInput())
	require.ErrorIs(t, err, ErrGroupNotAllowed)
}

func TestValidateAdminManagedAPIKeyForEnsureRejectsReplayDrift(t *testing.T) {
	input := managedAPIKeyInput()
	groupID := input.GroupID
	expiresAt := time.Now().UTC().Add(time.Hour)
	key := &APIKey{
		ID: 11, UserID: 7, Key: "sk-existing", Name: input.Name,
		GroupID: &groupID, Status: StatusActive, ExpiresAt: &expiresAt,
	}
	require.NoError(t, ValidateAdminManagedAPIKeyForEnsure(key, 7, input))

	key.Status = StatusDisabled
	require.ErrorIs(t, ValidateAdminManagedAPIKeyForEnsure(key, 7, input), ErrAPIKeyUnusable)
	key.Status = StatusActive
	key.Name = "other-name"
	require.ErrorIs(t, ValidateAdminManagedAPIKeyForEnsure(key, 7, input), ErrAdminManagedAPIKeyReplayUnavailable)
}
