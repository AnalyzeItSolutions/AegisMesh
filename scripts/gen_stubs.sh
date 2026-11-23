#!/usr/bin/env bash
# Generates Go and Python stubs from proto/ into gen/go and gen/py. Never edit the output by hand.
set -euo pipefail
cd "$(dirname "$0")/.."
GO_MODULE="github.com/aegismesh/aegismesh/gen"
PROTOS=$(find proto -name '*.proto' | sort)

# Go: module github.com/aegismesh/aegismesh/gen at gen/go, package aegismeshv1 in gen/go/aegismeshv1
export PATH="$PATH:$(go env GOPATH)/bin"
command -v protoc-gen-go >/dev/null || go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
command -v protoc-gen-go-grpc >/dev/null || go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
rm -rf gen/go/aegismeshv1
mkdir -p gen/go
protoc --proto_path=proto --go_out=gen/go --go_opt=module="$GO_MODULE" \
       --go-grpc_out=gen/go --go-grpc_opt=module="$GO_MODULE" $PROTOS
if [ ! -s gen/go/go.mod ]; then
  printf 'module %s\n\ngo 1.22\n' "$GO_MODULE" > gen/go/go.mod
fi
(cd gen/go && go mod tidy)

# Python: gen/py/aegismesh/v1/*_pb2.py and *_pb2_grpc.py
python3 -c 'import grpc_tools' 2>/dev/null || python3 -m pip install -q grpcio grpcio-tools
rm -rf gen/py/aegismesh
mkdir -p gen/py
python3 -m grpc_tools.protoc --proto_path=proto --python_out=gen/py --pyi_out=gen/py --grpc_python_out=gen/py $PROTOS
echo "stubs generated: $(find gen/go -name '*.pb.go' | wc -l | tr -d ' ') go files, $(find gen/py -name '*_pb2*.py' | wc -l | tr -d ' ') python files"
