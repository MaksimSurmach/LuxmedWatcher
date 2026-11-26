package miniapp

import (
	"crypto/rand"
	"sync"
	"time"
)

const maxHistoryViews = 128

type historyView struct {
	userID    int64
	expiresAt time.Time
	ids       []int64
}

// historyViews retains bounded, immutable slot orders. Status and visibility
// still come from the database on each request.
type historyViews struct {
	mu    sync.Mutex
	views map[string]historyView
}

func (h *historyViews) save(userID int64, now time.Time, ids []int64) string {
	token := rand.Text()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.views == nil {
		h.views = make(map[string]historyView)
	}
	var oldestToken string
	var oldest time.Time
	for key, view := range h.views {
		if !now.Before(view.expiresAt) {
			delete(h.views, key)
		} else if oldestToken == "" || view.expiresAt.Before(oldest) {
			oldestToken, oldest = key, view.expiresAt
		}
	}
	if len(h.views) >= maxHistoryViews {
		delete(h.views, oldestToken)
	}
	h.views[token] = historyView{userID: userID, expiresAt: now.Add(dashboardSessionLifetime), ids: ids}
	return token
}

func (h *historyViews) load(token string, userID int64, now time.Time) ([]int64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	view, ok := h.views[token]
	if !ok || view.userID != userID {
		return nil, false
	}
	if !now.Before(view.expiresAt) {
		delete(h.views, token)
		return nil, false
	}
	return view.ids, true
}
