package bot

import (
	"context"
	"strconv"
	"testing"

	"github.com/maksimsurmach/luxmed-watcher/internal/domain"
	"github.com/maksimsurmach/luxmed-watcher/internal/storage"
)

// A forged callback must not touch another user's watch. api is nil, so reaching
// any send would also panic.
func TestWatchActionRejectsOtherUsersWatch(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir()+"/test.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 1, TelegramChatID: 1, Locale: "en", Status: domain.UserStatusActive, Role: domain.UserRoleUser})
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := db.UpsertTelegramUser(ctx, domain.User{TelegramUserID: 2, TelegramChatID: 2, Locale: "en", Status: domain.UserStatusActive, Role: domain.UserRoleUser})
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateWatch(ctx, domain.Watch{UserID: owner.ID, Name: "w", Status: domain.WatchStatusActive, CheckIntervalSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}

	b := &Bot{db: db}
	for _, action := range []string{"pause", "delete", "resume", "show"} {
		b.handleWatchAction(ctx, attacker, "watch:"+action+":"+strconv.FormatInt(id, 10))
	}

	watch, err := db.Watch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if watch.Status != domain.WatchStatusActive {
		t.Fatalf("attacker changed status to %q", watch.Status)
	}
}
