module github.com/aegismesh/aegismesh/services/telemetry-go

go 1.26.0

replace github.com/aegismesh/aegismesh/gen => ../../gen/go

require (
	github.com/aegismesh/aegismesh/gen v0.0.0-00010101000000-000000000000
	github.com/alicebob/miniredis/v2 v2.40.0
	github.com/coder/websocket v1.8.15
	github.com/redis/go-redis/v9 v9.23.0
	github.com/twmb/franz-go v1.22.3
	github.com/uber/h3-go/v4 v4.6.0
	google.golang.org/grpc v1.85.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.15.1 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260817212433-ac3dfec99bb1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
