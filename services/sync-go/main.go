package main

import (
	"context"
	"log"
	"net"
	"net/http"
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
	srv := NewServer(node)
	pb.RegisterSyncServiceServer(g, srv)
	httpAddr := os.Getenv("HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8051"
	}
	hs := &http.Server{Addr: httpAddr, Handler: srv.HTTPHandler()}
	go func() {
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http %s: %v", httpAddr, err)
		}
	}()
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		<-c
		_ = hs.Shutdown(context.Background())
		g.GracefulStop()
	}()
	log.Printf("sync service: gRPC %s, HTTP %s, node %s", addr, httpAddr, node)
	if err := g.Serve(lis); err != nil {
		log.Fatal(err)
	}
}
