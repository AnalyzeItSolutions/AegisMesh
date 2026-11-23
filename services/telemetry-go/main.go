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
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

type memGeo struct{}

func (memGeo) Put(context.Context, string, Frame) error { return nil }

type discardPublisher struct{}

func (discardPublisher) Publish(context.Context, string, string, []byte) error { return nil }

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fan := NewFanout()

	var geo GeoStore = memGeo{}
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		host, _ := os.Hostname()
		rg := NewRedisGeo(redis.NewClient(&redis.Options{Addr: addr}), host+"-"+time.Now().Format("150405.000"))
		geo = rg
		go func() {
			if err := rg.Listen(ctx, fan); err != nil {
				log.Printf("redis feed stopped: %v", err)
			}
		}()
	} else {
		log.Printf("REDIS_ADDR is not set: positions are not stored and other instances are not fed")
	}
	var pub Publisher = discardPublisher{}
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		kp, err := NewKafkaPublisher(strings.Split(brokers, ","))
		if err != nil {
			log.Fatalf("kafka: %v", err)
		}
		defer kp.Close()
		pub = kp
	} else {
		log.Printf("KAFKA_BROKERS is not set: positions are not published to the log")
	}
	srv := NewServer(geo, pub, fan)

	lis, err := net.Listen("tcp", env("GRPC_ADDR", ":50054"))
	if err != nil {
		log.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterTelemetryServiceServer(g, srv)
	mux := http.NewServeMux()
	mux.Handle("/ws", srv.WSHandler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }) //nolint:errcheck
	hs := &http.Server{Addr: env("HTTP_ADDR", ":8084"), Handler: mux}
	go func() {
		<-ctx.Done()
		g.GracefulStop()
		hs.Shutdown(context.Background()) //nolint:errcheck
	}()
	go func() {
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	log.Printf("telemetry service: gRPC %s, WebSocket %s/ws", lis.Addr(), hs.Addr)
	if err := g.Serve(lis); err != nil {
		log.Fatal(err)
	}
}
