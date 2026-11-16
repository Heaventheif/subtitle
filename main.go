// نقطة الإقلاع: خادم الترجمات v3 بلغة Go — بلا AI ولا ترجمة آلية.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg := LoadConfig()
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("فتح المخزن: %v", err)
	}
	defer store.Close()

	srv := NewServer(cfg, store)
	httpServer := &http.Server{
		Addr:              ":" + itoa(cfg.Port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("subtitle-server v3 (Go) listening on :%d", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("تشغيل الخادم: %v", err)
		}
	}()

	// إيقاف سلس عند SIGTERM (Render)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("إيقاف...")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)
}

func itoa(n int) string {
	return fmtInt(n)
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
