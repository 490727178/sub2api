#!/usr/bin/env bash
# =============================================================================
# 本地快速迭代：重新编译后端（内嵌前端）-> 注入容器 -> 重启 -> 健康检查
# =============================================================================
# 为什么需要这个脚本：
#   前端资源靠 //go:embed all:dist 打包进二进制，编译必须同时满足两点：
#     1. backend/internal/web/dist 里已有前端产物（先跑 pnpm build）
#     2. go build 带上 -tags embed
#   手动编译很容易漏掉 -tags embed，结果是页面全部 404（API 却正常）。
#
# 用法：
#   ./rebuild-local.sh                 # 复用已有 dist，只重编译后端
#   ./rebuild-local.sh --frontend      # 先重建前端，再编译后端
#   ./rebuild-local.sh --no-restart    # 只替换二进制，不重启容器
#   ./rebuild-local.sh --help
#
# 环境变量：
#   CONTAINER   目标容器名（默认 sub2api-local）
#   PORT        宿主机访问端口，用于健康检查（默认 18080）
#   GOPROXY     模块代理（默认 https://goproxy.cn,direct）
#   PNPM_REGISTRY  前端依赖源（默认 https://registry.npmmirror.com）
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

CONTAINER="${CONTAINER:-sub2api-local}"
PORT="${PORT:-18080}"
GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
PNPM_REGISTRY="${PNPM_REGISTRY:-https://registry.npmmirror.com}"

BUILD_FRONTEND="auto"
DO_RESTART="yes"

usage() {
    sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

for arg in "$@"; do
    case "$arg" in
        --frontend)      BUILD_FRONTEND="always" ;;
        --skip-frontend) BUILD_FRONTEND="never" ;;
        --no-restart)    DO_RESTART="no" ;;
        -h|--help)       usage; exit 0 ;;
        *) echo "未知参数: $arg（用 --help 查看用法）" >&2; exit 2 ;;
    esac
done

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m错误:\033[0m %s\n' "$*" >&2; exit 1; }

# --- 0. 前置检查 -------------------------------------------------------------
[[ "$(docker inspect -f '{{.State.Running}}' "${CONTAINER}" 2>/dev/null || true)" == "true" ]] \
    || die "容器 ${CONTAINER} 未运行，先把它启动起来"

case "$(docker exec "${CONTAINER}" uname -m)" in
    aarch64|arm64) GOARCH="arm64" ;;
    x86_64|amd64)  GOARCH="amd64" ;;
    *) die "无法识别的容器架构" ;;
esac
log "目标容器 ${CONTAINER}（linux/${GOARCH}）"

DIST_DIR="${REPO_ROOT}/backend/internal/web/dist"

# --- 1. 前端构建（按需） -----------------------------------------------------
if [[ "${BUILD_FRONTEND}" == "always" || ( "${BUILD_FRONTEND}" == "auto" && ! -f "${DIST_DIR}/index.html" ) ]]; then
    log "构建前端 -> ${DIST_DIR}"
    if [[ "$(pnpm --version 2>/dev/null || echo none)" != 9.* ]]; then
        log "切换 pnpm 到 9.15.9（项目 lockfile 需要 pnpm 9）"
        corepack prepare pnpm@9.15.9 --activate
    fi
    if [[ ! -d "${REPO_ROOT}/frontend/node_modules" ]]; then
        log "安装前端依赖"
        (cd "${REPO_ROOT}/frontend" && pnpm install --frozen-lockfile --registry="${PNPM_REGISTRY}")
    fi
    (cd "${REPO_ROOT}/frontend" && pnpm run build)
else
    log "复用已有前端产物（加 --frontend 可强制重建）"
fi

[[ -f "${DIST_DIR}/index.html" ]] || die "缺少 ${DIST_DIR}/index.html，请加 --frontend 重建前端"

# --- 2. 编译后端（必须带 embed） ---------------------------------------------
BIN="$(mktemp -t sub2api-embed)"
log "编译后端 -tags embed"
(cd "${REPO_ROOT}/backend" && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" GOPROXY="${GOPROXY}" \
    go build -tags embed -ldflags="-s -w" -o "${BIN}" ./cmd/server)

grep -qa "Frontend not embedded" "${BIN}" && die "编译产物没有内嵌前端，检查 -tags embed 是否生效"
[[ "$(grep -ac "assets/index-" "${BIN}" || true)" -gt 0 ]] || die "编译产物里找不到前端资源，dist 可能不完整"
log "产物校验通过（$(du -h "${BIN}" | cut -f1)）"

# --- 3. 注入容器 -------------------------------------------------------------
BACKUP="/tmp/${CONTAINER}-$(date +%Y%m%d-%H%M%S).bak"
docker cp "${CONTAINER}:/app/sub2api" "${BACKUP}"
log "旧二进制已备份到 ${BACKUP}"

docker cp "${BIN}" "${CONTAINER}:/app/sub2api"
docker exec "${CONTAINER}" chmod +x /app/sub2api
rm -f "${BIN}"

# --- 4. 重启 + 健康检查 ------------------------------------------------------
if [[ "${DO_RESTART}" == "yes" ]]; then
    log "重启容器"
    docker restart "${CONTAINER}" >/dev/null

    for _ in $(seq 1 30); do
        if curl -fsS -m 3 "http://127.0.0.1:${PORT}/health" >/dev/null 2>&1; then
            break
        fi
        sleep 2
    done

    curl -fsS -m 5 "http://127.0.0.1:${PORT}/health" >/dev/null || die "健康检查失败，看日志：docker logs ${CONTAINER}"
    HOME_CODE="$(curl -sS -m 5 -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/")"
    [[ "${HOME_CODE}" == "200" ]] || die "首页返回 ${HOME_CODE}，前端可能没生效"
    log "完成：/health 与 / 均正常（http://127.0.0.1:${PORT}）"
else
    log "已替换二进制但未重启，自行执行：docker restart ${CONTAINER}"
fi
