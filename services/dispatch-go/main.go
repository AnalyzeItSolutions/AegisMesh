package main

import (
	"context"
	"log"
	"net"
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
	ob := NewOutbox(store, pub, NewMemCursors())
	go ob.Run(ctx, time.Second)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	g := grpc.NewServer()
	pb.RegisterDispatchServiceServer(g, NewServer(NewDispatcher(store, NewLocks()), ob))
	go func() {
		<-ctx.Done()
		g.GracefulStop()
	}()
	log.Printf("dispatch service on %s", addr)
	if err := g.Serve(lis); err != nil {
		log.Fatal(err)
	}
}
