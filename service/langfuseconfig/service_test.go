package langfuseconfig

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/langfuse"
	"github.com/QuantumNous/new-api/setting/langfuse_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useLangfuseConfigDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	previousOptionMap := common.OptionMap
	previousDatabaseType := common.MainDatabaseType()
	previousBinding := langfuse.LoadBinding()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))

	model.DB = db
	common.OptionMap = map[string]string{}
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	// Publish the disabled default so the active snapshot version matches the
	// manager's counter; per-test version deltas are then exact.
	require.NoError(t, langfuse.PublishSnapshot(langfuse_setting.DefaultLangfuseSetting))
	t.Cleanup(func() {
		model.DB = previousDB
		common.OptionMap = previousOptionMap
		common.SetMainDatabaseType(previousDatabaseType)
		langfuse.PublishBinding(*previousBinding)
	})
	return db
}

func enabledSetting() langfuse_setting.LangfuseSetting {
	s := langfuse_setting.DefaultLangfuseSetting
	s.Enabled = true
	s.Host = "https://x.example/langfuse/"
	s.PublicKey = "pk-lf-1"
	s.SecretKey = "sk-lf-1"
	s.SampleRate = 0.3
	s.SendContent = true
	s.SessionHeaderNames = []string{"X-Conversation-Id"}
	s.SessionBodyPaths = []string{"metadata.session_id"}
	return s
}

func enableRequest() UpdateRequest {
	s := enabledSetting()
	return UpdateRequest{
		Enabled:                 &s.Enabled,
		Host:                    s.Host,
		PublicKey:               s.PublicKey,
		SecretKey:               s.SecretKey,
		Environment:             s.Environment,
		SampleRate:              &s.SampleRate,
		SendContent:             &s.SendContent,
		MaxContentBytes:         s.MaxContentBytes,
		MaxResponseBytes:        s.MaxResponseBytes,
		MaxInFlightCaptureBytes: s.MaxInFlightCaptureBytes,
		MaxSessionBodyBytes:     s.MaxSessionBodyBytes,
		SessionHeaderNames:      s.SessionHeaderNames,
		SessionBodyPaths:        s.SessionBodyPaths,
		QueueSize:               s.QueueSize,
		BatchSize:               s.BatchSize,
		FlushIntervalSeconds:    s.FlushIntervalSeconds,
	}
}

func seedPersistedSetting(t *testing.T, s langfuse_setting.LangfuseSetting) {
	t.Helper()
	values, err := optionValues(s)
	require.NoError(t, err)
	require.NoError(t, model.UpdateOptionsBulk(values))
}

func TestReconcilePublishesDisabledDefaultWhenNothingPersisted(t *testing.T) {
	useLangfuseConfigDB(t)
	require.NoError(t, langfuse.PublishSnapshot(enabledSetting()))
	require.True(t, langfuse.LoadBinding().Snapshot.Enabled)

	require.NoError(t, Reconcile())

	snapshot := langfuse.LoadBinding().Snapshot
	assert.False(t, snapshot.Enabled)
	assert.Equal(t, "", snapshot.TracesURL)
	assert.Equal(t, 0.1, snapshot.SampleRate)
}

func TestReconcilePublishesPersistedSetAndSkipsUnchangedConfiguration(t *testing.T) {
	useLangfuseConfigDB(t)
	// Simulates another instance committing the full set to the shared database.
	seedPersistedSetting(t, enabledSetting())
	versionBefore := langfuse.CurrentVersion()

	require.NoError(t, Reconcile())

	snapshot := langfuse.LoadBinding().Snapshot
	assert.True(t, snapshot.Enabled)
	assert.Equal(t, "https://x.example/langfuse/api/public/otel/v1/traces", snapshot.TracesURL)
	assert.Equal(t, []string{"X-Conversation-Id"}, snapshot.SessionHeaderNames)
	assert.Equal(t, versionBefore+1, snapshot.Version)

	// An unchanged configuration must not rebuild the runtime.
	require.NoError(t, Reconcile())
	assert.Equal(t, versionBefore+1, langfuse.CurrentVersion())
}

func TestReconcileKeepsBindingWhenPersistedSetIsInvalid(t *testing.T) {
	useLangfuseConfigDB(t)
	invalid := enabledSetting()
	invalid.SecretKey = ""
	seedPersistedSetting(t, invalid)
	before := langfuse.LoadBinding()

	err := Reconcile()

	assert.Error(t, err)
	assert.Same(t, before, langfuse.LoadBinding())
}

func TestUpdatePersistsWholeGroupAndPublishesOnce(t *testing.T) {
	useLangfuseConfigDB(t)
	versionBefore := langfuse.CurrentVersion()

	require.NoError(t, Update(enableRequest()))

	stored, err := model.AllOptionsByPrefix(langfuse_setting.OptionKeyPrefix)
	require.NoError(t, err)
	assert.Len(t, stored, 16)
	assert.Equal(t, "sk-lf-1", stored["langfuse_setting.secret_key"])
	assert.Equal(t, "0.3", stored["langfuse_setting.sample_rate"])

	snapshot := langfuse.LoadBinding().Snapshot
	assert.True(t, snapshot.Enabled)
	assert.Equal(t, "https://x.example/langfuse/api/public/otel/v1/traces", snapshot.TracesURL)
	assert.Equal(t, versionBefore+1, snapshot.Version)
}

func TestUpdateRejectsInvalidCandidateWithoutSideEffects(t *testing.T) {
	useLangfuseConfigDB(t)
	before := langfuse.LoadBinding()

	req := enableRequest()
	req.Host = "langfuse:3000"
	assert.Error(t, Update(req))

	stored, err := model.AllOptionsByPrefix(langfuse_setting.OptionKeyPrefix)
	require.NoError(t, err)
	assert.Empty(t, stored)
	assert.Same(t, before, langfuse.LoadBinding())
}

func TestUpdateKeepsBindingWhenPersistenceFails(t *testing.T) {
	db := useLangfuseConfigDB(t)
	before := langfuse.LoadBinding()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	err = Update(enableRequest())

	assert.ErrorIs(t, err, ErrStorage)
	assert.Same(t, before, langfuse.LoadBinding())
}

func TestUpdateAndReconcileConvergeUnderConcurrency(t *testing.T) {
	useLangfuseConfigDB(t)
	require.NoError(t, Update(enableRequest()))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			assert.NoError(t, Update(enableRequest()))
		}()
		go func() {
			defer wg.Done()
			assert.NoError(t, Reconcile())
		}()
	}
	wg.Wait()

	// Both paths write and read the same complete set, so no ordering can leave
	// the binding on a different configuration.
	snapshot := langfuse.LoadBinding().Snapshot
	assert.True(t, snapshot.Enabled)
	assert.Equal(t, "https://x.example/langfuse/api/public/otel/v1/traces", snapshot.TracesURL)
	assert.Equal(t, 0.3, snapshot.SampleRate)
	assert.Equal(t, snapshot.Version, langfuse.CurrentVersion())
}
