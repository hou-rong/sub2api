//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newEnsureAPIKeyRepoTest(t *testing.T) (context.Context, *dbent.Client, *apiKeyRepository, int64, int64) {
	t.Helper()
	dsn := fmt.Sprintf("file:api_key_ensure_%d?mode=memory&cache=shared&_fk=1", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	driver := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(driver)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	user, err := client.User.Create().
		SetEmail("managed-key@example.com").
		SetPasswordHash("hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().
		SetName("managed-group").
		SetPlatform(service.PlatformOpenAI).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetRateMultiplier(1).
		Save(ctx)
	require.NoError(t, err)
	return ctx, client, newAPIKeyRepositoryWithSQL(client, db), user.ID, group.ID
}

func TestEnsureByUserIDAndNameConcurrentCreateProducesOneKey(t *testing.T) {
	ctx, client, repo, userID, groupID := newEnsureAPIKeyRepoTest(t)
	const workers = 12
	var createdCount atomic.Int64
	ids := make(chan int64, workers)
	errs := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			expiresAt := time.Now().UTC().AddDate(0, 0, 365)
			candidate := &service.APIKey{
				UserID: userID, Key: fmt.Sprintf("sk-concurrent-%02d", index),
				Name: "employee-zhishu-client", GroupID: &groupID,
				Status: service.StatusActive, ExpiresAt: &expiresAt,
			}
			key, created, err := repo.EnsureByUserIDAndName(ctx, candidate)
			if err != nil {
				errs <- err
				return
			}
			if created {
				createdCount.Add(1)
			}
			ids <- key.ID
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		require.NoError(t, err)
	}
	var expectedID int64
	for id := range ids {
		if expectedID == 0 {
			expectedID = id
		}
		require.Equal(t, expectedID, id)
	}
	require.Equal(t, int64(1), createdCount.Load())
	count, err := client.APIKey.Query().Where(
		apikey.UserIDEQ(userID),
		apikey.NameEQ("employee-zhishu-client"),
		apikey.DeletedAtIsNil(),
	).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestEnsureByUserIDAndNameRejectsHistoricalDuplicates(t *testing.T) {
	ctx, client, repo, userID, groupID := newEnsureAPIKeyRepoTest(t)
	for i := 0; i < 2; i++ {
		_, err := client.APIKey.Create().
			SetUserID(userID).
			SetKey(fmt.Sprintf("sk-duplicate-%d", i)).
			SetName("duplicate-name").
			SetGroupID(groupID).
			SetStatus(service.StatusActive).
			Save(ctx)
		require.NoError(t, err)
	}
	candidate := &service.APIKey{
		UserID: userID, Key: "sk-never-created", Name: "duplicate-name",
		GroupID: &groupID, Status: service.StatusActive,
	}
	_, _, err := repo.EnsureByUserIDAndName(ctx, candidate)
	require.ErrorIs(t, err, service.ErrAPIKeyNameConflict)
}
