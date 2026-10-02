package account

import "time"

// unixTime converts a unix-seconds value to a UTC time for GORM range
// filters.
func unixTime(ts int64) time.Time {
	return time.Unix(ts, 0).UTC()
}