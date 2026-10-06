package admin

import (
	"crypto/sha256"
	"fmt"
	"time"

	"trae-relay/internal/account"
)

const (
	queryTimeout       = 30 * time.Second
	updateTimeout      = 30 * time.Second
	updateApplyTimeout = 10 * time.Minute
	allCheckinTimeout  = 30 * time.Minute
)

func deviceIDOf(a account.Account) string {
	identity := a.UserID
	if identity == "" {
		identity = a.ID
	}
	return account.CheckinDeviceID(identity, a.CheckinGen)
}

// shortHash 用于展示（未使用可删除）。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:4])
}
