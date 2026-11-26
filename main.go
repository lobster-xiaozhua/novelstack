// novelstack：私人小说在线阅读站（第一至第四阶段）。
//
// 子命令：
//
//	novelstack [serve]             启动 HTTP 服务（默认）
//	novelstack backup create ...   加密备份
//	novelstack restore-drill ...   恢复演练
//	novelstack kek rotate ...      KEK 轮换
//	novelstack import ...          批量导入
//	novelstack cache prune ...     缓存淘汰
package main

import (
	"os"

	"novelstack/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
