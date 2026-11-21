package main

import (
	"cmp"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/maksimsurmach/luxmed-watcher/internal/luxmed"
	"github.com/maksimsurmach/luxmed-watcher/internal/miniapp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	botID, _ := strconv.ParseInt(os.Getenv("LUXMED_BOT_ID"), 10, 64)
	workers, err := luxmed.ParseBrowserRoutes(os.Getenv("LUXMED_BROWSER_ROUTES"))
	if err != nil {
		log.Fatal(err)
	}
	if os.Getenv("LUXMED_BROWSER_SHARED") == "1" {
		if workers != nil {
			log.Fatal("shared workers take account IDs from LUXMED_BROWSER_ACCOUNT_IDS")
		}
		workers, err = luxmed.SharedBrowserRoutes(os.Getenv("LUXMED_BROWSER_API_URL"), os.Getenv("LUXMED_BROWSER_VIEWER_URL"),
			cmp.Or(os.Getenv("LUXMED_BROWSER_ACCOUNT_IDS"), os.Getenv("ADMIN_TELEGRAM_IDS")))
		if err != nil {
			log.Fatal(err)
		}
	}
	if workers == nil {
		ownerID, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv("ADMIN_TELEGRAM_IDS")), 10, 64)
		workers = map[int64]luxmed.BrowserRoute{
			ownerID: {API: os.Getenv("LUXMED_BROWSER_API_URL"), Viewer: os.Getenv("LUXMED_BROWSER_VIEWER_URL")},
		}
	}
	public, private, err := miniapp.New(miniapp.Config{
		Origin:       strings.TrimRight(os.Getenv("LUXMED_MINIAPP_URL"), "/"),
		ViewerOrigin: os.Getenv("LUXMED_VIEWER_URL"), BotID: botID, Workers: workers,
		DashboardAPI: os.Getenv("LUXMED_DASHBOARD_API_URL"),
		TicketKey:    strings.TrimSpace(os.Getenv("LUXMED_INTERNAL_KEY")),
		WorkerKey:    strings.TrimSpace(os.Getenv("LUXMED_WORKER_KEY")),
	}, os.DirFS("/app/web"), os.DirFS("/app/vendor"))
	if err != nil {
		log.Fatal(err)
	}
	go serve(ctx, ":8093", private)
	serve(ctx, ":8090", public)
}

func serve(ctx context.Context, addr string, handler http.Handler) {
	server := &http.Server{
		Addr: addr, Handler: handler, ReadHeaderTimeout: 4 * time.Second, ReadTimeout: 8 * time.Second,
		WriteTimeout: 22 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	context.AfterFunc(ctx, func() { server.Close() })
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
