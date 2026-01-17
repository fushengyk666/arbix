# Arbix - 加密货币市场监控与报警系统

Arbix 是一个基于 Go 语言开发的高性能加密货币市场监控系统。它能够实时采集交易所（目前支持 Binance）的市场数据，根据预设的策略进行分析，并通过 Telegram 或 Webhook 发送报警通知。

## ✨ 主要功能

*   **实时数据采集**: 支持 Binance 现货 (Spot) 和合约 (Futures) 的 WebSocket 及 REST API 数据采集。
*   **灵活的策略配置**: 支持自定义监控策略，例如波动率监测 (Volatility) 和大趋势监测 (Big Change)。
*   **多渠道报警**: 支持通过 Telegram Bot 和 Webhook 发送报警通知。
*   **高性能架构**: 采用微服务架构，使用 NATS 作为消息中间件，解耦数据采集 (Collector) 和 监控分析 (Monitor) 模块。
*   **容器化部署**: 提供完整的 Docker 和 Docker Compose 支持，一键启动。

## 🏗️ 架构概览

系统主要由以下组件构成：

1.  **Collector (采集器)**:
    *   负责连接交易所 (Binance)。
    *   实时接收行情数据 (Ticker, Trade, Kline 等)。
    *   将标准化后的数据发布到 NATS 消息队列。

2.  **Monitor (监控器)**:
    *   订阅 NATS 中的市场数据。
    *   加载 `configs/config.yaml` 中的策略配置。
    *   实时计算并在触发阈值时通过指定渠道 (Telegram/Webhook) 发送报警。

3.  **NATS**:
    *   高性能消息中间件，用于连接 Collector 和 Monitor。

## 🚀 快速开始

### 前置要求

*   [Docker](https://www.docker.com/)
*   [Docker Compose](https://docs.docker.com/compose/)

### 1. 克隆项目

```bash
git clone <your-repo-url>
cd arbix
```

### 2. 配置环境变量

复制示例环境配置文件：

```bash
cp .env.example .env
```

编辑 `.env` 文件，填入必要的配置信息（例如 Telegram Bot Token）：

```dotenv
# Telegram 配置
TG_1_TOKEN=your_bot_token
TG_1_CHAT_ID=your_chat_id

# 其他配置...
```

### 3. 修改策略配置 (可选)

监控策略和报警渠道在 `configs/config.yaml` 中定义。你可以根据需要调整阈值、时间窗口或报警渠道。

### 4. 启动服务

使用 Docker Compose 启动所有服务：

```bash
docker-compose up -d
```

查看日志确认运行状态：

```bash
docker-compose logs -f
```

## ⚙️ 配置说明

### 核心配置 (`configs/config.yaml`)

*   **nats**: NATS 连接配置。
*   **binance**: 交易所接口地址和连接参数。
*   **channels**: 定义报警发送渠道（Telegram, Webhook 等）。
*   **strategies**: 定义监控逻辑。
    *   `volatility`: 波动率策略，监控短时间内的剧烈价格波动。
    *   `big_change`: 大趋势策略，监控较长时间窗口内的涨跌幅。

### 环境变量 (`.env`)

敏感信息通过环境变量注入，具体字段请参考 `.env.example`。

## 🛠️ 开发与构建

如果要本地修改源码并运行：

1.  确保本地安装了 Go 1.20+。
2.  运行 Collector:
    ```bash
    go run cmd/collector/main.go -config configs/config.yaml
    ```
3.  运行 Monitor:
    ```bash
    go run cmd/monitor/main.go -config configs/config.yaml
    ```

## 📄 License

[MIT License](LICENSE)
