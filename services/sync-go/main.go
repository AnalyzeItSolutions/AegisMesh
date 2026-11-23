package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	"google.golang.org/grpc"
)

func main() {
	addr := os.Getenv("GRPC_ADDR")
	if addr == "" {
		addr = ":50051"
	}
	node := os.Getenv("NODE_ID")
	if node == "" {
		node, _ = os.Hostname()
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	g := grpc.NewServer()
	pb.RegisterSyncServiceServer(g, NewServer(node))
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		<-c
		g.GracefulStop()
	}()
	log.Printf("sync service on %s as %s", addr, node)
	if err := g.Serve(lis); err != nil {
		log.Fatal(err)
	}
}
