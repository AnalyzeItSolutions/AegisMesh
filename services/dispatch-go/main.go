package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"google.golang.org/grpc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	addr := os.Getenv("GRPC_ADDR")
	if addr == "" {
		addr = ":50052"
	}
	store, err := StoreFromEnv(ctx)
	if err != nil {
		log.Fatalf("event store: %v", err)
	}
	var pub Publisher = &discardPublisher{}
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		kp, err := NewKafkaPublisher(strings.Split(brokers, ","))
		if err != nil {
			log.Fatalf("kafka: %v", err)
		}
		defer kp.Close()
		pub = kp
	}
	cursors := NewMemCursors()
	if pg, ok := store.(*PGStore); ok {
		cursors = pg.Cursors() // survives a restart: no republishing of the whole log
	}
	ob := NewOutbox(store, pub, cursors)
	go ob.Run(ctx, time.Second)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	g := grpc.NewServer()
	srv := NewServer(NewDispatcher(store, NewLocks()), ob)
	pb.RegisterDispatchServiceServer(g, srv)
	httpAddr := os.Getenv("HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8052"
	}
	hs := &http.Server{Addr: httpAddr, Handler: srv.HTTPHandler()}
	go func() {
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http %s: %v", httpAddr, err)
		}
	}()
	go func() {
		<-ctx.Done()
		_ = hs.Shutdown(context.Background())
		g.GracefulStop()
	}()
	log.Printf("dispatch service: gRPC %s, HTTP %s", addr, httpAddr)
	if err := g.Serve(lis); err != nil {
		log.Fatal(err)
	}
}
