package storage

import (
	"os"
	"strconv"
	"time"
)

const (
	defaultRetention  = 7
	defaultShadowTest = "default"
	retentionInterval = time.Hour
)

func retentionDaysFromEnv() int {
	if v := os.Getenv("BERU_DB_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultRetention
}

func shadowTestNameFromEnv() string {
	if v := os.Getenv("BERU_SHADOW_TEST_NAME"); v != "" {
		return v
	}
	return defaultShadowTest
}
