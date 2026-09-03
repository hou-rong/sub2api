package repository

import (
	"context"
	"errors"
	"fmt"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/internal/service"
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
