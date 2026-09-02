package main

import (
	"context"
	"time"

	"github.com/morehao/ark-forge/internal/server"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/golib/glog"
)

func main() {
	// manager 落地前，先用 mock 领域服务与模拟事件源让控制面可独立起服冒烟。
	svc := handler.NewMockService()
	bus := eventbus.New()

	go mockEventFeed(bus)

	srv := server.New(server.Config{Addr: ":8099"}, svc, bus)
	if err := srv.Start(); err != nil {
		panic(err)
	}
}

// mockEventFeed 周期性向总线发布会话事件，模拟后续 ACP 的实时 notification 流。
func mockEventFeed(bus *eventbus.Bus) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		bus.Publish("message", "s1", 1, map[string]any{"delta": "hello from mock feed"})
		glog.Info(context.Background(), "mock: published event")
	}
}
