package main

import (
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

	"github.com/maksimsurmach/luxmed-watcher/internal/miniapp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	botID, _ := strconv.ParseInt(os.Getenv("LUXMED_BOT_ID"), 10, 64)
	ownerID, _ := strconv.ParseInt(os.Getenv("ADMIN_TELEGRAM_IDS"), 10, 64)
	public, private, err := miniapp.New(miniapp.Config{
		Origin:    strings.TrimRight(os.Getenv("LUXMED_MINIAPP_URL"), "/"),
		WorkerAPI: os.Getenv("LUXMED_BROWSER_API_URL"), WorkerViewer: os.Getenv("LUXMED_BROWSER_VIEWER_URL"),
		ViewerOrigin: os.Getenv("LUXMED_VIEWER_URL"), BotID: botID, OwnerID: ownerID,
		DashboardAPI: os.Getenv("LUXMED_DASHBOARD_API_URL"),
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
