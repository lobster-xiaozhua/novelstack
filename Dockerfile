# 多阶段构建：最终镜像 distroless/static，CGO_ENABLED=0（modernc.org/sqlite 纯 Go 驱动）。
# 注：如需 BuildKit 前端语法，可加 # syntax=docker/dockerfile:1（此处省略以避免额外拉取镜像）

# 运行镜像：distroless/static 含 CA 证书与 tzdata，无 shell、无包管理器。
# 若网络拉不到 gcr.io（或需要内网镜像），用 --build-arg 覆盖即可，例如
#   RUNTIME_IMAGE=registry.local/distroless/static-debian12:nonroot
# 该 ARG 必须在第一个 FROM 之前声明（global scope），否则 FROM 取不到值。
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

FROM golang:1.22-alpine AS build
WORKDIR /src
# 模块代理（可按需覆盖：--build-arg GOPROXY=direct）
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

# 先拷贝依赖清单，利用层缓存下载依赖
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 控制台前端产物由 //go:embed 打包进二进制（仓库内已提交 internal/adminui/dist）。
# 修改前端后请先 make web 再构建镜像；缺失时立即失败，避免发布没有控制台的镜像。
RUN test -f internal/adminui/dist/index.html || \
    (echo "缺少 internal/adminui/dist：请先执行 make web（需要 pnpm）" >&2; exit 1)
# CGO_ENABLED=0：纯 Go 编译，适配 distroless/scratch
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/novelstack .
# 运行镜像需要可写临时目录：恢复演练（restore-drill）会在 os.TempDir() 下解包。
# distroless 不一定自带 /tmp，这里显式准备一个 1777 的目录随镜像带入。
RUN mkdir -p /out/tmp && chmod 1777 /out/tmp

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/novelstack /app/novelstack
COPY --from=build --chmod=1777 /out/tmp /tmp

# 数据目录（实际读写来自 docker volume/bind mount）
VOLUME ["/data"]
EXPOSE 8080
# 默认 serve；备份 / 演练 / 轮换 / 导入走 docker compose run --rm app /app/novelstack <子命令>
ENTRYPOINT ["/app/novelstack"]
