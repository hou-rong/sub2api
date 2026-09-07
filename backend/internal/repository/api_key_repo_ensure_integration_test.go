//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/userallowedgroup"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestEnsureByUserIDAndNamePostgresCrossConnectionConcurrency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminClient := testEntClient(t)
	unique := fmt.Sprintf("%d", time.Now().UnixNano())
	managedKeyName := "employee-zhishu-client-" + unique
	user, err := adminClient.User.Create().
		SetEmail("managed-key-cross-connection-" + unique + "@example.com").
		SetPasswordHash("test-password-hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	group, err := adminClient.Group.Create().
		SetName("managed-key-cross-connection-" + unique).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = adminClient.APIKey.Delete().Where(apikey.UserIDEQ(user.ID)).Exec(cleanupCtx)
		_ = adminClient.User.DeleteOneID(user.ID).Exec(cleanupCtx)
		_ = adminClient.Group.DeleteOneID(group.ID).Exec(cleanupCtx)
	})

	// Keep the first insert transaction open long enough to verify that the
	// repository acquired its transaction-scoped PostgreSQL advisory lock. The
	// trigger is isolated to this test's unique managed-key name.
	delayFunction := "test_managed_key_delay_" + unique
	delayTrigger := "test_managed_key_delay_trigger_" + unique
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger AS $$
BEGIN
    PERFORM pg_sleep(0.5);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql`, pq.QuoteIdentifier(delayFunction)))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = integrationDB.ExecContext(cleanupCtx, "DROP FUNCTION IF EXISTS "+pq.QuoteIdentifier(delayFunction)+"()")
	})
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`
CREATE TRIGGER %s
BEFORE INSERT ON api_keys
FOR EACH ROW
WHEN (NEW.name = %s)
EXECUTE FUNCTION %s()`,
		pq.QuoteIdentifier(delayTrigger),
		pq.QuoteLiteral(managedKeyName),
		pq.QuoteIdentifier(delayFunction),
	))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = integrationDB.ExecContext(cleanupCtx,
			"DROP TRIGGER IF EXISTS "+pq.QuoteIdentifier(delayTrigger)+" ON api_keys")
	})

	const workers = 8
	repositories := make([]*apiKeyRepository, 0, workers)
	clients := make([]*dbent.Client, 0, workers)
	backendPIDs := make(map[int]struct{}, workers)
	for i := 0; i < workers; i++ {
		db, openErr := sql.Open("postgres", integrationPostgresDSN)
		require.NoError(t, openErr)
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		require.NoError(t, db.PingContext(ctx))

		var backendPID int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
		_, duplicatePID := backendPIDs[backendPID]
		require.False(t, duplicatePID, "each repository must use an independent PostgreSQL backend")
		backendPIDs[backendPID] = struct{}{}

		driver := entsql.OpenDB(dialect.Postgres, db)
		client := dbent.NewClient(dbent.Driver(driver))
		clients = append(clients, client)
		repositories = append(repositories, newAPIKeyRepositoryWithSQL(client, db))
	}
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.Close()
		}
	})
	require.Len(t, backendPIDs, workers)

	type ensureResult struct {
		key     *service.APIKey
		created bool
		err     error
	}
	results := make(chan ensureResult, workers)
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(workers)
	done.Add(workers)
	for i, repo := range repositories {
		go func(index int, workerRepo *apiKeyRepository) {
			defer done.Done()
			ready.Done()
			<-start

			expiresAt := time.Now().UTC().AddDate(0, 0, 365)
			candidate := &service.APIKey{
				UserID:    user.ID,
				Key:       fmt.Sprintf("sk-cross-connection-%s-%02d", unique, index),
				Name:      managedKeyName,
				GroupID:   &group.ID,
				Status:    service.StatusActive,
				ExpiresAt: &expiresAt,
			}
			key, created, ensureErr := workerRepo.EnsureByUserIDAndName(ctx, candidate)
			results <- ensureResult{key: key, created: created, err: ensureErr}
		}(i, repo)
	}
	ready.Wait()
	close(start)

	lockKey := fmt.Sprintf("managed-api-key:%d:%s", user.ID, managedKeyName)
	lockObserved := false
	lockObservationDeadline := time.Now().Add(2 * time.Second)
	for !lockObserved && time.Now().Before(lockObservationDeadline) {
		probeTx, beginErr := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		var acquired bool
		probeErr := probeTx.QueryRowContext(ctx,
			"SELECT pg_try_advisory_xact_lock($1)", advisoryLockHash(lockKey)).Scan(&acquired)
		require.NoError(t, probeErr)
		require.NoError(t, probeTx.Rollback())
		lockObserved = !acquired
		if !lockObserved {
			time.Sleep(10 * time.Millisecond)
		}
	}
	require.True(t, lockObserved, "expected another PostgreSQL connection to hold the managed-key advisory lock")

	done.Wait()
	close(results)

	var (
		createdCount int
		ensuredID    int64
		ensuredKey   string
	)
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.key)
		if result.created {
			createdCount++
		}
		if ensuredID == 0 {
			ensuredID = result.key.ID
			ensuredKey = result.key.Key
		}
		require.Equal(t, ensuredID, result.key.ID)
		require.Equal(t, ensuredKey, result.key.Key)
	}
	require.Equal(t, 1, createdCount)

	keys, err := adminClient.APIKey.Query().Where(
		apikey.UserIDEQ(user.ID),
		apikey.NameEQ(managedKeyName),
		apikey.DeletedAtIsNil(),
	).All(ctx)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, ensuredID, keys[0].ID)
	require.Equal(t, ensuredKey, keys[0].Key)
}

func TestEnsureByUserIDAndNameSerializesConcurrentAuthorizationRevocation(t *testing.T) {
	tests := []struct {
		name      string
		updateSQL string
		wantErr   error
	}{
		{
			name:      "user disabled",
			updateSQL: "UPDATE users SET status = 'disabled' WHERE id = $1",
			wantErr:   service.ErrAdminManagedUserInactive,
		},
		{
			name:      "group disabled",
			updateSQL: "UPDATE groups SET status = 'disabled' WHERE id = $1",
			wantErr:   service.ErrGroupNotAllowed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			adminClient := testEntClient(t)
			unique := fmt.Sprintf("%d", time.Now().UnixNano())
			user, err := adminClient.User.Create().
				SetEmail("managed-key-revocation-" + unique + "@example.com").
				SetPasswordHash("test-password-hash").
				SetRole(service.RoleUser).
				SetStatus(service.StatusActive).
				Save(ctx)
			require.NoError(t, err)
			group, err := adminClient.Group.Create().
				SetName("managed-key-revocation-" + unique).
				SetStatus(service.StatusActive).
				Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cleanupCancel()
				_, _ = adminClient.APIKey.Delete().Where(apikey.UserIDEQ(user.ID)).Exec(cleanupCtx)
				_ = adminClient.User.DeleteOneID(user.ID).Exec(cleanupCtx)
				_ = adminClient.Group.DeleteOneID(group.ID).Exec(cleanupCtx)
			})

			workerDB, err := sql.Open("postgres", integrationPostgresDSN)
			require.NoError(t, err)
			workerDB.SetMaxOpenConns(1)
			workerDB.SetMaxIdleConns(1)
			require.NoError(t, workerDB.PingContext(ctx))
			workerDriver := entsql.OpenDB(dialect.Postgres, workerDB)
			workerClient := dbent.NewClient(dbent.Driver(workerDriver))
			workerRepo := newAPIKeyRepositoryWithSQL(workerClient, workerDB)
			t.Cleanup(func() { _ = workerClient.Close() })

			revocationTx, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = revocationTx.Rollback() })
			targetID := user.ID
			if tc.name == "group disabled" {
				targetID = group.ID
			}
			result, err := revocationTx.ExecContext(ctx, tc.updateSQL, targetID)
			require.NoError(t, err)
			rowsAffected, err := result.RowsAffected()
			require.NoError(t, err)
			require.Equal(t, int64(1), rowsAffected)

			type ensureResult struct {
				key     *service.APIKey
				created bool
				err     error
			}
			resultCh := make(chan ensureResult, 1)
			expiresAt := time.Now().UTC().AddDate(0, 0, 365)
			managedKeyName := "employee-revocation-" + unique
			go func() {
				candidate := &service.APIKey{
					UserID: user.ID, Key: "sk-revocation-" + unique,
					Name: managedKeyName, GroupID: &group.ID,
					Status: service.StatusActive, ExpiresAt: &expiresAt,
				}
				key, created, ensureErr := workerRepo.EnsureByUserIDAndName(ctx, candidate)
				resultCh <- ensureResult{key: key, created: created, err: ensureErr}
			}()

			lockKey := fmt.Sprintf("managed-api-key:%d:%s", user.ID, managedKeyName)
			lockObserved := false
			lockObservationDeadline := time.Now().Add(2 * time.Second)
			for !lockObserved && time.Now().Before(lockObservationDeadline) {
				select {
				case early := <-resultCh:
					require.Failf(t, "ensure returned before revocation committed", "result: %+v", early)
				default:
				}
				probeTx, beginErr := integrationDB.BeginTx(ctx, nil)
				require.NoError(t, beginErr)
				var acquired bool
				probeErr := probeTx.QueryRowContext(ctx,
					"SELECT pg_try_advisory_xact_lock($1)", advisoryLockHash(lockKey)).Scan(&acquired)
				require.NoError(t, probeErr)
				require.NoError(t, probeTx.Rollback())
				lockObserved = !acquired
				if !lockObserved {
					time.Sleep(10 * time.Millisecond)
				}
			}
			require.True(t, lockObserved, "expected ensure to hold its advisory lock while waiting on revocation")
			select {
			case early := <-resultCh:
				require.Failf(t, "ensure returned while revocation remained uncommitted", "result: %+v", early)
			default:
			}

			require.NoError(t, revocationTx.Commit())
			ensured := <-resultCh
			require.ErrorIs(t, ensured.err, tc.wantErr)
			require.Nil(t, ensured.key)
			require.False(t, ensured.created)

			count, err := adminClient.APIKey.Query().Where(
				apikey.UserIDEQ(user.ID),
				apikey.NameEQ(managedKeyName),
				apikey.DeletedAtIsNil(),
			).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count)
		})
	}
}

func TestEnsureByUserIDAndNamePostgresAuthorizationSnapshots(t *testing.T) {
	tests := []struct {
		name           string
		configureGroup func(*dbent.GroupCreate) *dbent.GroupCreate
		grantAccess    func(context.Context, *dbent.Client, int64, int64) error
	}{
		{
			name: "exclusive group",
			configureGroup: func(builder *dbent.GroupCreate) *dbent.GroupCreate {
				return builder.SetIsExclusive(true).SetSubscriptionType(service.SubscriptionTypeStandard)
			},
			grantAccess: func(ctx context.Context, client *dbent.Client, userID, groupID int64) error {
				_, err := client.UserAllowedGroup.Create().SetUserID(userID).SetGroupID(groupID).Save(ctx)
				return err
			},
		},
		{
			name: "subscription group",
			configureGroup: func(builder *dbent.GroupCreate) *dbent.GroupCreate {
				return builder.SetSubscriptionType(service.SubscriptionTypeSubscription)
			},
			grantAccess: func(ctx context.Context, client *dbent.Client, userID, groupID int64) error {
				_, err := client.UserSubscription.Create().
					SetUserID(userID).
					SetGroupID(groupID).
					SetStatus(service.SubscriptionStatusActive).
					SetStartsAt(time.Now().UTC().Add(-time.Hour)).
					SetExpiresAt(time.Now().UTC().Add(24 * time.Hour)).
					Save(ctx)
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			client := testEntClient(t)
			unique := fmt.Sprintf("%d", time.Now().UnixNano())
			user, err := client.User.Create().
				SetEmail("managed-key-access-" + unique + "@example.com").
				SetPasswordHash("test-password-hash").
				SetRole(service.RoleUser).
				SetStatus(service.StatusActive).
				Save(ctx)
			require.NoError(t, err)
			group, err := tc.configureGroup(client.Group.Create().
				SetName("managed-key-access-" + unique).
				SetStatus(service.StatusActive)).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cleanupCancel()
				_, _ = client.APIKey.Delete().Where(apikey.UserIDEQ(user.ID)).Exec(cleanupCtx)
				_, _ = client.UserAllowedGroup.Delete().Where(
					userallowedgroup.UserIDEQ(user.ID),
					userallowedgroup.GroupIDEQ(group.ID),
				).Exec(cleanupCtx)
				_, _ = client.UserSubscription.Delete().Where(
					usersubscription.UserIDEQ(user.ID),
					usersubscription.GroupIDEQ(group.ID),
				).Exec(cleanupCtx)
				_ = client.User.DeleteOneID(user.ID).Exec(cleanupCtx)
				_ = client.Group.DeleteOneID(group.ID).Exec(cleanupCtx)
			})

			repo := newAPIKeyRepositoryWithSQL(client, integrationDB)
			expiresAt := time.Now().UTC().AddDate(0, 0, 365)
			candidate := &service.APIKey{
				UserID: user.ID, Key: "sk-access-" + unique,
				Name: "employee-access-" + unique, GroupID: &group.ID,
				Status: service.StatusActive, ExpiresAt: &expiresAt,
			}
			_, _, err = repo.EnsureByUserIDAndName(ctx, candidate)
			require.ErrorIs(t, err, service.ErrGroupNotAllowed)
			require.NoError(t, tc.grantAccess(ctx, client, user.ID, group.ID))

			ensured, created, err := repo.EnsureByUserIDAndName(ctx, candidate)
			require.NoError(t, err)
			require.True(t, created)
			require.NotNil(t, ensured)
			require.Equal(t, candidate.Name, ensured.Name)
		})
	}
}
