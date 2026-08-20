package model

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func useUsernameLookupDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	previousRedis := common.RedisEnabled

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))

	DB = db
	common.RedisEnabled = false
	t.Cleanup(func() {
		DB = previousDB
		common.RedisEnabled = previousRedis
	})
}

func TestGetUserIDByUsernameMatchesExactly(t *testing.T) {
	useUsernameLookupDB(t)
	user := User{Username: "Alice"}
	require.NoError(t, DB.Create(&user).Error)

	id, err := GetUserIDByUsername("Alice")
	require.NoError(t, err)
	assert.Equal(t, user.Id, id)

	_, err = GetUserIDByUsername("alice")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestGetUserIDByUsernameReturnsNotFound(t *testing.T) {
	useUsernameLookupDB(t)

	for _, username := range []string{"missing", ""} {
		_, err := GetUserIDByUsername(username)
		assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	}
}

func TestGetUserIDByUsernameSkipsSoftDeletedUsers(t *testing.T) {
	useUsernameLookupDB(t)
	user := User{Username: "deleted-user"}
	require.NoError(t, DB.Create(&user).Error)
	require.NoError(t, DB.Delete(&user).Error)

	_, err := GetUserIDByUsername(user.Username)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
