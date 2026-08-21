// irs 是 images-repo-sync 平台的命令行客户端。
//
// 用法概览:
//
//	irs login --server http://localhost:8080 --username admin --password ***
//	irs registries list
//	irs tasks create --source 1 --target 2 --mode flat --refs nginx:1.25 --wait
//	irs charts upload 1 ./mychart-0.1.0.tgz
//
// 完整文档见仓库 docs/cli.md。
package main

import (
	"os"

	"images-repo-sync/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
