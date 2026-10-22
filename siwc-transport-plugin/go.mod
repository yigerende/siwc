module siwc-transport-plugin

go 1.27.2

require (
	github.com/Wei-Shaw/sub2api v0.2.17
	github.com/hashicorp/go-plugin v1.8.0
	github.com/tidwall/gjson v1.18.0
	github.com/tidwall/sjson v1.2.5
	google.golang.org/grpc v1.83.2
)

require (
	github.com/fatih/color v1.18.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// 本地开发：直接引用同目录下改造过的 sub2api（协议定义不变，
// 发布时可改为公开版本号）。
replace github.com/Wei-Shaw/sub2api => ../sub2api/backend
