# gocurl

Gocurl 是一个 curl 风格的 Go HTTP 客户端，默认使用
[tls-client](https://github.com/bogdanfinn/tls-client)（基于 uTLS）发送
Chrome 152 的 TLS/HTTP2 指纹和请求头，适合授权 Web/API 安全测试、自动化
请求和需要浏览器指纹的场景。

支持 `HTTP`、`HTTPS`、`FTP`、`FTPS`、`WS`、`WSS`；其他协议会明确报
`Protocol ... not supported`，不会静默降级。

> 仅用于已获得授权的安全测试、资产自查和调试。请自行确认目标范围与授权。

## 特性

- Chrome 152 TLS/HTTP2 指纹，默认请求头和头顺序与浏览器一致。
- curl 风格的参数和常用输出格式，便于替换现有自动化脚本。
- HTTP/HTTPS 代理、环境代理、SOCKS、`--resolve`、`--connect-to`、
  Unix Socket 和源网卡绑定。
- Cookie、重定向、Digest/NTLM/netrc、客户端证书、SSE 流式响应。
- FTP/FTPS 下载、列表、上传；支持主动模式、显式/隐式 FTPS 和断点续传。
- DNS 可指定自定义 resolver 或 DoH，避免依赖系统 DNS。
- WS/WSS handshake 和简单消息收发。
- 默认后台 daemon 复用连接并共享 Cookie；支持 `--session` 隔离。
- `%{json}` / `--json-output` 输出结构化请求指标；`--json-envelope` 输出请求/
  响应/证书/耗时等完整 envelope；`--raw-request` 发送原样 HTTP 请求字节。
- 常见 multipart 文件上传流式处理，避免大文件整体驻留内存。
- `--next` 支持在一次调用中执行多组不同配置的请求。

## 快速开始

```sh
make build
./gocurl -sS https://example.com/
./gocurl -sS -H 'Accept: application/json' https://httpbin.org/get
./gocurl -sS --json-output -o body.bin https://example.com/api
```

安装到 PATH（可选）：

```sh
install -m 0755 gocurl /usr/local/bin/gocurl
```

## 构建

需要 Go 1.27+。

```sh
make build          # 生成 ./gocurl
make test
make vet
```

不使用 Makefile 时：

```sh
go build -trimpath -o gocurl ./cmd/gocurl
```

构建出的 `gocurl` 二进制默认被 `.gitignore` 忽略。

## 目录结构

```text
.
├── cmd/gocurl/          # 仅进程入口
├── internal/
│   ├── app/             # 参数组调度、run/--next、退出码、envelope
│   ├── config/          # Options、参数解析、表单和 resolver/DNS 解析
│   ├── exitcode/        # curl 风格错误码映射
│   ├── transport/       # dial、DNS/DoH、代理、TLS/证书、resolve
│   ├── tlsname/         # TLS 版本/cipher 名称映射
│   ├── httpx/           # HTTP client、请求构造、认证、Cookie、重定向、h2c
│   ├── output/          # body/header、write-out、流式解码、限速
│   ├── ftp/             # FTP/FTPS、主动/被动模式、续传
│   ├── websocket/       # WS/WSS
│   └── daemon/          # daemon 协议、客户端、服务端
├── Makefile
├── README.md
├── go.mod
└── go.sum
```

依赖方向保持单向：`app` 依赖各领域包；`httpx`/`ftp`/`websocket` 依赖
`config`、`transport`、`output`；`transport` 和 `config` 不反向依赖应用层。
CLI/集成测试目前集中在 `internal/app`，后续可在各包内补充单元测试。

## 支持范围

| 类别 | 支持 |
| --- | --- |
| HTTP 版本 | HTTP/1.1、HTTP/2、h2c（`--http2-prior-knowledge`） |
| 协议 | HTTP、HTTPS、FTP、FTPS、WS、WSS |
| 认证 | Basic、Digest、NTLM、netrc、`--anyauth`；`--negotiate` 为 NTLM token fallback |
| 代理 | HTTP、HTTPS、SOCKS4、SOCKS4a、SOCKS5、SOCKS5h、preproxy |
| 定向 | `--resolve`、`--connect-to`、`--interface`、Unix Socket |
| 传输 | 流式响应、SSE、Range、断点续传、上传、限速、最大文件限制 |
| 输出 | 文本、文件、响应头、`-w` 指标、JSON |

## 常用参数

完整列表见 `./gocurl --help`。

### 请求与响应

```text
-X, --request <method>        请求方法
    --request-target <target> 自定义请求目标（如 * 或绝对 URI）
-H, --header <header>         添加/覆盖请求头，可重复
    --header-order <names>    自定义请求头顺序
-A, --user-agent <string>     User-Agent
-e, --referer <url>           Referer
-b, --cookie <string|file>    Cookie 字符串或 Netscape cookie 文件
-c, --cookie-jar <file>       保存 Netscape 格式 Cookie
-d, --data <data>             请求体，隐含 POST
    --data-raw <data>         不解析 @file
    --data-binary <data>      保留换行
    --data-urlencode <data>   URL 编码表单字段
    --json <data>             JSON 请求体，隐含 POST
-F, --form <name=value>       multipart 表单字段
    --form-string <name=value> 字面值表单字段
-G, --get                     把数据放进 URL query
-i, --include                 输出响应头
-I, --head                    HEAD 请求并输出响应头
-D, --dump-header <file>      响应头写入文件
-o, --output <file>           输出到文件
-O, --remote-name             使用 URL 最后一段作为文件名
    --create-dirs             自动创建输出目录
-l, --list-only               FTP/FTPS 只列文件名
-P, --ftp-port <address>      主动模式数据连接监听地址，传 "-" 使用默认地址
    --ftp-pasv                 强制被动模式（默认）
    --ftp-ssl                  显式 FTPS，通过 AUTH TLS 升级控制连接
-T, --upload-file <file>      上传文件（HTTP 流式、FTP/FTPS STOR）
-N, --no-buffer               禁用输出缓冲
```

### 网络、代理与 TLS

```text
-x, --proxy <url>             HTTP/HTTPS/SOCKS 代理
-U, --proxy-user <user:pass>  代理认证
    --proxy-header <header>   代理 CONNECT 附加头
    --proxy-insecure          跳过 HTTPS 代理证书校验
    --preproxy <url>          连接最终代理所使用的代理
    --noproxy <hosts>         代理绕过列表
    --resolve <host:port:addr[,addr]>
    --connect-to <h:p:h:p>
    --unix-socket <path>
    --abstract-unix-socket <name>
    --interface <name|ip>
    --dns-servers <servers>    自定义 DNS，逗号分隔，可指定端口
    --doh-url <url>            DNS-over-HTTPS resolver
    --doh-insecure             跳过 DoH TLS 证书校验
    --deny-private-addresses   拒绝环回/私网/链路本地目标（默认关闭）
-4, --ipv4                    只使用 IPv4
-6, --ipv6                    只使用 IPv6
-k, --insecure                跳过目标证书校验
    --cacert <file>           自定义 CA
    --capath <dir>            CA 目录
-E, --cert <file>             客户端证书（PEM/P12/DER）
    --key <file>              客户端私钥
    --cert-type <type>        PEM、P12、DER
    --key-type <type>         PEM、DER
    --pass <phrase>           私钥/P12 密码
```

TLS 指纹固定为 Chrome 152；`--tlsv1.x`、`--tls-max`、`--ciphers`、`--curves`、
`--pinnedpubkey`、`--cert-status` 不支持。`--capath` 只加载自定义 CA，不改变指纹。

`--dns-servers` 和 `--doh-url` 同时指定时 DoH 优先；`--resolve`/`--connect-to`
优先于两种 DNS 配置。DoH 使用 HTTPS + POST（`application/dns-message`），
`--doh-insecure` 只跳过 TLS 校验，不会允许明文 HTTP DoH。

显式 `-x` 优先；未指定时读取小写 `http_proxy` 以及 `HTTPS_PROXY`、`FTP_PROXY`、
`ALL_PROXY`，用 `NO_PROXY` 或 `--noproxy` 控制绕过（支持 `host:port`）。
出于 CGI 环境安全考虑，大写 `HTTP_PROXY` 不会被读取。HTTP/HTTPS/SOCKS 代理
同样适用于 FTP/FTPS 和 WS/WSS。`--proxy-header` 只发送给最终代理，不发送给
`--preproxy` 前置代理。

### 认证与重定向

```text
-u, --user <user:pass>        Basic Auth
    --digest                  Digest Auth
    --ntlm                    NTLM Auth
    --negotiate               Negotiate（NTLM token fallback）
    --anyauth                 自动选择 Basic/Digest/NTLM
    --netrc                   读取 ~/.netrc
    --netrc-file <file>       指定 netrc 文件
-L, --location                跟随重定向
    --location-trusted        跨主机转发 Authorization（Cookie 仍不跨主机）
    --post301/302/303         保持 POST 方法跨重定向
    --max-redirs <num>        最大重定向次数，默认 50
```

### 重试、传输与协议

```text
-f, --fail                    HTTP >= 400 时返回 22
    --fail-with-body          HTTP >= 400 时保留响应体并返回 22
    --retry <num>             失败重试次数
    --retry-delay <seconds>   重试间隔，默认指数退避
    --retry-max-time <seconds> 重试总时间
    --retry-all-errors        重试连接和传输阶段错误（不含响应体中段截断）
    --retry-connrefused       连接被拒绝时也重试
    --retry-idempotent        只重试 GET/HEAD/PUT/DELETE/OPTIONS/TRACE
    --retry-max-per-host <n>  每个主机名的重试总预算，0 表示不限
-r, --range <range>           HTTP Range
    --continue-at <offset|->  断点续传（HTTP/FTP）
    --limit-rate <rate>       限制上传/下载速率
    --max-filesize <size>     超过大小返回 63
    --compressed              请求并解码压缩响应
    --raw                    不解码 Content-Encoding
    --tr-encoding             请求压缩 Transfer-Encoding
    --http1.0                 原始 HTTP/1.0 模式
    --http1.1                 强制 HTTP/1.1
    --http2                   优先 HTTP/2，失败回退 HTTP/1.1
    --http2-prior-knowledge   h2c 明文 HTTP/2
    --raw-http                显式原始 HTTP/1.x 模式
    --raw-request <file>      发送文件中的原样 HTTP 请求字节
```

### 会话、并行与输出

```text
-Z, --parallel                并行传输多个 URL
    --parallel-max <num>      最大并行数，默认 50
    --session <name>          隔离 daemon 的 Cookie 和连接池
    --no-persist              禁用后台 daemon
    --next                    开始下一组独立配置的请求
    --max-time <seconds>      总超时
    --connect-timeout <sec>   连接超时
-w, --write-out <format>      输出统计变量，支持 %{json}
    --json-output             传输结束后输出 %{json}
    --json-envelope           输出一条完整 JSON envelope
    --show-secrets            不在 verbose/envelope 中打码凭据
-v, --verbose                 输出请求/响应和 TLS 信息
-K, --config <file>           读取 curl 风格配置文件
-s, --silent                  静默模式
-S, --show-error              静默模式下仍显示错误
```

支持的 `--write-out` 变量：

```text
%{http_code} %{response_code} %{http_version} %{scheme}
%{url_effective} %{size_download} %{size_header}
%{time_total} %{time_connect} %{time_appconnect} %{time_starttransfer}
%{num_redirects} %{num_retries} %{content_type} %{remote_ip}
%{ssl_verify_result} %{tls_version} %{alpn} %{json}
```

## 常用示例

```sh
# 普通请求
./gocurl -sS https://example.com/
./gocurl -sS -X POST -H 'Content-Type: application/json' -d '{"a":1}' https://example.com/api

# JSON 请求和结构化输出
./gocurl -sS --json '{"a":1}' https://httpbin.org/post
./gocurl -sS -o body.bin --json-output https://example.com/api

# 表单和文件上传
./gocurl -sS -F 'name=value' -F 'file=@./a.txt;type=text/plain' https://httpbin.org/post
./gocurl -sS -T ./large.bin https://example.com/upload

# 代理与定向
./gocurl -sS -x http://127.0.0.1:8080 https://example.com
./gocurl -sS --resolve example.com:443:127.0.0.1 https://example.com/
./gocurl -sS --connect-to example.com:443:10.0.0.5:443 https://example.com/
./gocurl -sS --unix-socket /var/run/docker.sock http://unix/version
./gocurl -sS --interface lo https://example.com

# 认证
./gocurl -sS --digest -u user:pass https://example.com
./gocurl -sS --netrc-file ./netrc https://example.com

# 下载控制
./gocurl -sS -C - -o file.bin https://example.com/file
./gocurl -sS -r 0-1023 https://example.com/file
./gocurl -sS --limit-rate 1M --max-filesize 100M https://example.com/file

# 重试
./gocurl -sS --retry 3 --retry-delay 1 https://example.com/api
./gocurl -sS --retry 3 --retry-idempotent --retry-max-per-host 2 https://example.com/api

# JSON envelope 和结构化采集
./gocurl -sS --json-envelope https://example.com/api
./gocurl -sS --json-envelope -o body.bin https://example.com/api

# 原样 HTTP 请求
./gocurl -sS --raw-request request.txt http://127.0.0.1:8080/

# SSE
./gocurl -sS -N https://example.com/events

# HTTP/2 h2c 和原始 HTTP
./gocurl -sS --http2-prior-knowledge http://127.0.0.1:8080/api
./gocurl -sS --http1.0 http://127.0.0.1:8080/legacy

# FTP/WS
./gocurl -sS ftp://user:pass@ftp.example.com/file.txt
./gocurl -sS -P - ftp://user:pass@ftp.example.com/file.txt
./gocurl -sS -k --ftp-ssl ftp://user:pass@ftp.example.com/file.txt
./gocurl -sS -C - -o file.bin ftp://user:pass@ftp.example.com/file.txt
./gocurl -sS -x socks5://127.0.0.1:1080 ftp://user:pass@ftp.example.com/file.txt
./gocurl -sS -d 'hello' ws://127.0.0.1:8080/echo
./gocurl -sS -k -d 'hello' wss://example.com/ws

# DNS / DoH
./gocurl -sS --dns-servers 1.1.1.1,8.8.8.8:53 https://example.com/
./gocurl -sS --doh-url https://dns.google/dns-query https://example.com/

# 一次调用执行多组请求
./gocurl -sS https://example.com/a --next -sS -X POST -d 'x=1' https://example.com/b

# 会话隔离
./gocurl -sS --session pentest-a -b cookies.txt https://example.com
./gocurl --stop-daemon --session pentest-a
```

## JSON envelope 与 raw request

`--json-envelope` 每个 URL 输出一行 JSON，包含：

- request method/URL/request-target/headers/body 长度；
- response 状态、协议、headers、effective URL、重定向链、Set-Cookie；
- TLS 版本、ALPN、cipher、证书主题/颁发者/SAN/SHA-256；
- 连接、首字节和总耗时，以及实际重试次数；
- error code/分类，以及响应体。

响应体处理：

- 未指定 `-o/-O` 时，body 内联到 JSON；文本为 `text`，二进制为 `base64`，
  默认截断到 1 MiB 并标记 `truncated`。
- 指定 `-o/-O` 时，body 写入文件，JSON 只记录 `file` 和大小。
- `-i`/`-I` 的响应头已在 JSON 中；`-w` 在 envelope 模式下不再输出。
- envelope 只支持 HTTP/HTTPS，不能和 raw HTTP 或 `--dump-header -` 组合。

`--raw-request <file>` 会原样发送文件中的 HTTP 请求字节，适合测试重复头、
大小写、异常路径和自定义请求行。URL 仍用于确定目标 host/port、TLS 和代理；
请求文件中的 method/headers/body 不会再经过 Gocurl 的默认头构造，`-X/-H/-d/-F/-T`
等构造参数也会被忽略。

```sh
./gocurl -sS --json-envelope https://example.com/api
./gocurl -sS --json-envelope -o body.bin https://example.com/api
./gocurl -sS --raw-request request.txt http://127.0.0.1:8080/
```

## SSE 与流式响应

HTTP 响应体按块读取并直接解码（gzip/deflate/br/zstd），不会等响应结束后才输出。
缓冲行为与 curl 一致：

- 管道/文件输出默认缓冲；`-N/--no-buffer` 逐块实时输出。
- 终端输出默认按行刷新。
- `--max-time` 超时保留已输出内容，返回 28，并报告已接收字节数。

```sh
./gocurl -sS -N https://example.com/events
```

## 连接持久化

默认启用后台 daemon：

- 首次调用时自动启动 detached daemon，通过 Unix domain socket 通信。
- 缓存 HTTP/1.1/HTTP/2 连接池；所有请求共享 Cookie jar。
- daemon 空闲 15 分钟后自动退出；Cookie 未写入 `-c` 文件会随进程丢失。
- `--session <name>` 使用独立 socket，隔离不同任务的 Cookie 和连接。
- `--no-persist` 禁用 daemon；daemon 启动失败时自动回退直连。
- daemon 只服务 HTTP/HTTPS；FTP/FTPS/WS/WSS 走直连。
- socket/日志默认在 `${XDG_RUNTIME_DIR:-/tmp}/gocurl/`，目录 `0700`、socket `0600`。

环境变量：

```text
GOCURL_NO_DAEMON=1        禁用 daemon
GOCURL_SOCKET=/path.sock  覆盖 socket 路径
GOCURL_SESSION=name       默认 session 名称
GOCURL_DAEMON_IDLE=30m    调整空闲退出时间
GOCURL_DAEMON_LOG=file    覆盖日志路径
```

## 指纹说明

- TLS 和 HTTP/2 使用 tls-client v1.17.1 的 `Chrome_152` profile。
- 默认实测 JA4：`t13d1517h2_8daaf6152771_cb7bf5808d99`；HTTP/2 Akamai：
  `1:65536;2:0;4:6291456;6:262144|15663105|0|m,a,s,p`。
- 默认头顺序和 HTTP/2 伪头顺序按 Chrome 152 设置；`-H` 可覆盖/追加，
  `--header-order` 可重排普通请求头。
- `--resolve`/`--connect-to` 只改变连接目标，不改变 Host/SNI/证书校验名。
- h2c 没有 TLS；原始 HTTP 模式（`--raw-http`/`--http1.0`）保留 Chrome uTLS
  ClientHello，但请求头和报文可控范围更大。
- TLS 指纹固定，不能通过 TLS 版本/cipher/pin/OCSP 选项切换；需要这些能力时用 curl。

## 与 curl 的差异和智能体使用建议

**Gocurl 不是 curl 的完整替代品。** 在 HTTP/HTTPS Web/API 渗透和浏览器指纹
请求这条主线上，可以作为智能体的默认客户端；它不是完整的渗透测试工具链。

**适合使用 Gocurl：**

- 常规 HTTP(S) 请求、登录会话、Cookie、重定向、Basic/Digest/NTLM。
- 需要 Chrome TLS/HTTP2 指纹或请求头顺序的请求。
- 虚拟主机定向、内网 Unix Socket、源网卡绑定。
- SSE、文件上传、范围/断点下载、限速、简单重试。
- 使用 `%{json}` 做结构化采集。

**继续使用 curl：**

- HTTP/3、TLS 版本/cipher/公钥 pin/OCSP 探测。
- 真正的 Kerberos/SPNEGO。
- 复杂 FTP/WS 语义（例如 FTP 主动模式配代理/FTPS）。
- 原始畸形 HTTP、请求走私、非 HTTP 协议。
- 依赖 curl 完整输出格式的既有脚本。

其他注意事项：

- 固定 Chrome 152 指纹一致性好，但所有 Gocurl 流量具有相同特征；通过 TLS
  中间人代理时，目标看到的是代理的指纹。
- FTP 主动模式目前不支持代理、FTPS 和 `--interface`；被动模式仍支持这些组合。
- `--dns-servers`/`--doh-url` 作用于直连和需要本地解析的目标；代理自身地址仍由系统解析。
- `--next` 每组参数独立，前一组选项不会自动延续到下一组；组间顺序执行，
  组内仍可使用 `--parallel`。
- 原始模式（`--raw-http`/`--http1.0`）不支持重定向、Digest/NTLM、重试和断点续传；
  `--raw-http` 会校验 method/request-target/header，任意字节请使用 `--raw-request`。
  `--raw-request` 不做请求语法校验，仅应在受控环境中使用。
- 常见 multipart 文件上传已流式处理；stdin、部分 encoder 场景会回退到内存，
  且仍不支持完整的 `multipart/mixed`。
- `--json-envelope` 只支持 HTTP/HTTPS；内联 body 默认截断到 1 MiB；默认对
  `Authorization`、`Cookie`、`Set-Cookie` 打码，`--show-secrets` 可关闭。
- `--continue-at` 只有在服务器返回匹配的 `206 Content-Range` 时才追加；否则返回 33，
  不会修改原文件。`--parallel` 的每组续传状态独立。
- 新建的输出文件、daemon 日志和普通 `-o` 文件默认 `0600`；普通 `-o` 已存在文件
  保持原权限。cookie jar 导出会强制 `0600`（已存在文件也收紧）；daemon 目录
  要求私有权限，普通输出路径拒绝符号链接，`/dev/std*` 与 `/dev/fd/*` 例外。
  daemon 只接受同 UID 的 Unix socket 连接。daemon 协议 v5 对无 Content-Length
  的响应使用分块结束帧，可区分正常结束与上游截断。
- `--parallel` 使用临时文件承接各组输出，避免把完整响应体同时驻留内存。
- 用户自定义 `Content-Length`/`Transfer-Encoding` 会被拒绝；需要特殊报文请使用
  `--raw-request`。`--raw-http` 也会校验 method、request-target 和 header。
- `COOKIE_JAR`/`-c` 只导出本次目标 host 相关的 cookie；`-b` 字符串只作用于当前请求，
  不写入共享 daemon jar。
- FTP 暂不支持 `--limit-rate`/`--max-filesize`；WS 不支持 `--limit-rate`，但支持
  `--max-filesize` 作为单条消息解压后大小与累计输出上限（单条默认 64 MiB），
  超限返回 63。FTP 暂不支持 `--netrc`。
- HTTP 的 `--max-filesize` 限制的是解码后的响应体大小；未设置时默认不设上限。
- `--deny-private-addresses` 默认关闭以保持 curl 兼容；开启后拒绝环回、私网、
  链路本地、未指定、组播、CGNAT、保留网段与 IPv4 内嵌 IPv6 目标（直连、daemon、
  DoH、自定义 DNS、`--resolve`/`--connect-to`）。带 zone 的 IPv6 字面量会先剥离
  zone 再判定。无法解析的地址按拒绝处理。`--doh-url` 必须是 HTTPS，且不接受
  userinfo 或 fragment。
- `--post301/302/303` 是显式保留方法/包体的开关，跨主机重定向时同样生效；
  不需要跨源重放时不要开启。`--no-location` 会同时清除 `--location-trusted`
  建立的状态。
- 配置文件的嵌套 include 以顶层配置文件目录为根，拒绝逃逸路径、符号链接、
  非普通文件；单文件与累计读取量、行数和展开参数数量均有上限。
- Linux 上普通输出路径使用 `openat2(RESOLVE_NO_SYMLINKS)` 拒绝父目录符号链接；
  其他平台至少拒绝末级符号链接。已存在的 `-o` 文件保持原权限，新建文件为 0600。
- `--negotiate` 是 NTLM fallback，不是完整 Kerberos；`--ntlm` 依赖连接保持。
- `--retry` 默认保持 curl 行为，会重试任意方法；非幂等智能体任务建议使用
  `--retry-idempotent`（只放行 GET/HEAD/PUT/DELETE/OPTIONS/TRACE）并配合
  `--retry-max-per-host` 限制每个主机名的重放预算。
- 多目标任务使用 `--session` 或独立 `GOCURL_SOCKET` 隔离 Cookie 和连接池。
- 主动 FTP 当前使用 `secsy/goftp`（2020 年伪版本），被动 FTP 使用
  `jlaffaye/ftp`；两者并存，升级或迁移前需分别回归主动/被动模式。

## 配置文件

Gocurl 不读取 `~/.curlrc`；需要用 `-K/--config` 指定 curl 风格配置：

```text
silent
header = "X-Test: yes"
url = "https://example.com/"
```

`-H @file` 可以从文件读取多行请求头。

## 测试

```sh
make test
make vet
```
