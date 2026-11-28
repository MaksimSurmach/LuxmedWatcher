package domain

import "time"

type Booking struct {
	ID        int64
	UserID    int64 `json:"-"`
	WatchID   int64
	DateTime  time.Time
	Service   string
	Doctor    string
	Place     string
	Automatic bool
	Status    string
	Reminders bool
}
