package model

import "errors"

// Common errors
var (
	ErrDatabase = errors.New("database error")
)

// User auth errors
var (
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrUserEmptyCredentials = errors.New("empty credentials")
	ErrEmailAlreadyTaken    = errors.New("email already taken")
	ErrEmailNotFound        = errors.New("email not found")
	ErrEmailAmbiguous       = errors.New("email matches multiple users")
)

// Token auth errors
var (
	ErrTokenNotProvided = errors.New("token not provided")
	ErrTokenInvalid     = errors.New("token invalid")
)

// Redemption errors
var ErrRedeemFailed = errors.New("redeem.failed")

// Subscription reset card errors
var (
	ErrNoAvailableResetCard         = errors.New("no available subscription reset card")
	ErrResetCardAlreadyUsed         = errors.New("subscription reset card already used")
	ErrResetCardInvalidSubscription = errors.New("invalid subscription for reset card")
	ErrResetCardNotFound            = errors.New("subscription reset card not found")
	ErrResetCardNotUnused           = errors.New("subscription reset card is not unused")
)

// 2FA errors
var ErrTwoFANotEnabled = errors.New("2fa not enabled")
var ErrTwoFAAlreadyEnabled = errors.New("2fa already enabled")
