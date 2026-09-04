package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	dbgroup "github.com/Wei-Shaw/sub2api/ent/group"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/userallowedgroup"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"entgo.io/ent/dialect"
)

// EnsureByUserIDAndName serializes the first creation of a managed API key by
// user and exact name. PostgreSQL uses a transaction-scoped advisory lock;
// non-PostgreSQL test stores additionally use the repository process lock.
func (r *apiKeyRepository) EnsureByUserIDAndName(
	ctx context.Context,
	candidate *service.APIKey,
) (*service.APIKey, bool, error) {
	if candidate == nil || candidate.UserID <= 0 || candidate.Name == "" {
		return nil, false, service.ErrAdminManagedAPIKeyEnsureUnavailable
	}

	var (
		txClient *dbent.Client
		txCtx    = ctx
		ownedTx  *dbent.Tx
	)
	if existingTx := dbent.TxFromContext(ctx); existingTx != nil {
		txClient = existingTx.Client()
	} else {
		tx, err := r.client.Tx(ctx)
		switch {
		case errors.Is(err, dbent.ErrTxStarted):
			txClient = r.client
		case err != nil:
			return nil, false, err
		default:
			ownedTx = tx
			defer func() { _ = ownedTx.Rollback() }()
			txClient = tx.Client()
			txCtx = dbent.NewTxContext(ctx, tx)
		}
	}

	release, err := lockRepositoryScopedKeys(
		txCtx,
		txClient,
		sqlExecutorFromEntClient(txClient),
		fmt.Sprintf("managed-api-key:%d:%s", candidate.UserID, candidate.Name),
	)
	if err != nil {
		return nil, false, err
	}
	defer release()
	if err := validateAdminManagedAPIKeyAccessLocked(txCtx, txClient, candidate); err != nil {
		return nil, false, err
	}

	existing, err := txClient.APIKey.Query().
		Where(
			apikey.UserIDEQ(candidate.UserID),
			apikey.NameEQ(candidate.Name),
			apikey.DeletedAtIsNil(),
		).
		Order(dbent.Asc(apikey.FieldID)).
		Limit(3).
		All(txCtx)
	if err != nil {
		return nil, false, err
	}
	if len(existing) > 1 {
		return nil, false, service.ErrAPIKeyNameConflict
	}
	if len(existing) == 1 {
		if ownedTx != nil {
			if err := ownedTx.Commit(); err != nil {
				return nil, false, err
			}
		}
		return apiKeyEntityToService(existing[0]), false, nil
	}

	builder := txClient.APIKey.Create().
		SetUserID(candidate.UserID).
		SetKey(candidate.Key).
		SetName(candidate.Name).
		SetStatus(candidate.Status).
		SetNillableGroupID(candidate.GroupID).
		SetQuota(candidate.Quota).
		SetQuotaUsed(candidate.QuotaUsed).
		SetNillableExpiresAt(candidate.ExpiresAt).
		SetRateLimit5h(candidate.RateLimit5h).
		SetRateLimit1d(candidate.RateLimit1d).
		SetRateLimit7d(candidate.RateLimit7d)
	if len(candidate.IPWhitelist) > 0 {
		builder.SetIPWhitelist(append([]string(nil), candidate.IPWhitelist...))
	}
	if len(candidate.IPBlacklist) > 0 {
		builder.SetIPBlacklist(append([]string(nil), candidate.IPBlacklist...))
	}
	created, err := builder.Save(txCtx)
	if err != nil {
		return nil, false, translatePersistenceError(err, nil, service.ErrAPIKeyExists)
	}
	if ownedTx != nil {
		if err := ownedTx.Commit(); err != nil {
			return nil, false, err
		}
	}
	return apiKeyEntityToService(created), true, nil
}

// validateAdminManagedAPIKeyAccessLocked makes authorization and key creation
// one transaction. PostgreSQL row locks serialize user/group/subscription
// revocation with this check; the process lock keeps non-PostgreSQL tests
// deterministic without emitting unsupported locking clauses.
func validateAdminManagedAPIKeyAccessLocked(
	ctx context.Context,
	client *dbent.Client,
	candidate *service.APIKey,
) error {
	if client == nil || candidate == nil || candidate.GroupID == nil || *candidate.GroupID <= 0 {
		return service.ErrAdminManagedAPIKeyEnsureUnavailable
	}

	postgres := client.Driver().Dialect() == dialect.Postgres
	userQuery := client.User.Query().Where(dbuser.IDEQ(candidate.UserID))
	if postgres {
		userQuery = userQuery.ForUpdate()
	}
	userEntity, err := userQuery.Only(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	userSnapshot := userEntityToService(userEntity)
	if err := service.ValidateAdminManagedAPIKeyUserSnapshot(userSnapshot); err != nil {
		return err
	}

	groupID := *candidate.GroupID
	groupQuery := client.Group.Query().Where(dbgroup.IDEQ(groupID))
	if postgres {
		groupQuery = groupQuery.ForUpdate()
	}
	groupEntity, err := groupQuery.Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrGroupNotAllowed
		}
		return err
	}

	groupSnapshot := groupEntityToService(groupEntity)
	subscriptionActive := false
	if groupSnapshot.IsSubscriptionType() {
		subscriptionQuery := client.UserSubscription.Query().
			Where(
				usersubscription.UserIDEQ(candidate.UserID),
				usersubscription.GroupIDEQ(groupID),
				usersubscription.StatusEQ(service.SubscriptionStatusActive),
				usersubscription.ExpiresAtGT(time.Now().UTC()),
			)
		if postgres {
			subscriptionQuery = subscriptionQuery.ForUpdate()
		}
		rows, queryErr := subscriptionQuery.Limit(2).All(ctx)
		if queryErr != nil {
			return queryErr
		}
		subscriptionActive = len(rows) == 1
		if len(rows) > 1 {
			return service.ErrGroupNotAllowed
		}
	} else if groupSnapshot.IsExclusive || userSnapshot.RestrictPublicGroups {
		allowedQuery := client.UserAllowedGroup.Query().
			Where(
				userallowedgroup.UserIDEQ(candidate.UserID),
				userallowedgroup.GroupIDEQ(groupID),
			)
		if postgres {
			allowedQuery = allowedQuery.ForUpdate()
		}
		rows, queryErr := allowedQuery.Limit(2).All(ctx)
		if queryErr != nil {
			return queryErr
		}
		if len(rows) == 1 {
			userSnapshot.AllowedGroups = []int64{groupID}
		}
		if len(rows) > 1 {
			return service.ErrGroupNotAllowed
		}
	}

	return service.ValidateAdminManagedAPIKeyProvisioningSnapshot(
		userSnapshot,
		groupSnapshot,
		subscriptionActive,
	)
}
