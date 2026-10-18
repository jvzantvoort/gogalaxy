package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/jvzantvoort/gogalaxy/internal/config"
	"github.com/jvzantvoort/gogalaxy/internal/model"
	"github.com/jvzantvoort/gogalaxy/internal/service"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
	_ "github.com/jvzantvoort/gogalaxy/internal/storage/file"
)

func TestBufferedDownloadCounts(t *testing.T) {
	v := viper.New()
	config.SetDefaults(v)
	v.Set("storage.path", t.TempDir())
	cfg, err := config.Load(v)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Roles().Save(ctx, &model.Role{Namespace: "ns", Name: "r"}); err != nil {
		t.Fatal(err)
	}
	svc := service.New(cfg, store, nil, nil)
	count := func() int64 {
		r, err := store.Roles().Get(ctx, "ns", "r")
		if err != nil {
			t.Fatal(err)
		}
		return r.DownloadCount
	}

	svc.CountRoleDownload(ctx, "ns", "r") // not buffered: written immediately
	if got := count(); got != 1 {
		t.Fatalf("unbuffered count = %d", got)
	}

	stop := svc.StartDownloadCounter(time.Hour)
	for range 5 {
		svc.CountRoleDownload(ctx, "ns", "r")
	}
	svc.CountRoleDownload(ctx, "ns", "missing") // ignored
	if got := count(); got != 1 {
		t.Fatalf("buffered counts written early: %d", got)
	}
	stop()
	if got := count(); got != 6 {
		t.Fatalf("count after final flush = %d, want 6", got)
	}
}
