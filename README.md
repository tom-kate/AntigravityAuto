# AntigravityAuto

Google 子号自动化管理工具 —— 批量处理 OAuth 授权、家庭组加入、手机绑定、额度监控。

## 功能

- **母号/子号管理**：添加母号，导入子号（最多 5 个/母号），支持 2FA
- **批次自动化**：一键启动批次，自动完成登录 → 家庭组确认 → OAuth 授权 → 额度检查 → CPA 检测 → 手机绑定全流程
- **家庭组加入**：自动在 Gmail 收件箱查找邀请邮件并确认加入，处理 Google 智能功能弹窗
- **额度监控**：对接 CPA 平台查询 Pro/Flash/Claude 模型额度，额度刷新超 5 小时自动判定账号死亡
- **手机绑定**：集成 SMS 平台，支持多频道自动切换、失败重试
- **CPA 凭证管理**：自动上传/删除凭证，支持额度查询和刷新
- **Web 管理面板**：暗色主题，实时状态更新，批次进度追踪
- **配置热重载**：修改配置无需重启，5 秒自动生效

## 自动化流程

```
登录 Gmail
  → 处理 TOTP / 辅助邮箱验证
  → 跳过恢复选项 / 家庭地址页面
  → 确认家庭组邀请邮件
  → OAuth 授权（最多重试 3 次）
  → 额度检查（任一模型 resetTime > 5h 判定死亡）
  → CPA 状态轮询
  → 手机绑定（如需要，多频道 SMS）
  → 删除旧凭证 → 重新 OAuth
  → 完成
```

## 本地运行

### 前置要求

- Go 1.25+
- Playwright Chromium（首次运行自动安装）

### 步骤

```bash
# 克隆仓库
git clone https://github.com/doitcan-oiu/AntigravityAuto.git
cd AntigravityAuto

# 复制配置文件并编辑
cp config.yaml.example data/config.yaml

# 编译运行
go build -o AntigravityAuto.exe .
./AntigravityAuto.exe
```

浏览器访问 `http://localhost:8080`

### 配置说明

编辑 `data/config.yaml`：

```yaml
proxy: http://127.0.0.1:10808    # 代理地址
proxy_enabled: false              # 是否启用代理
cpa_token: your-cpa-token        # CPA 平台 Token
cpa_api_url: http://127.0.0.1:8317  # CPA 平台地址
sms_username: your-username       # SMS 平台用户名
sms_password: your-password       # SMS 平台密码
sms_channel_ids:                  # SMS 频道 ID（按顺序尝试）
  - "channel-1"
  - "channel-2"
  - "channel-3"
headless: false                   # 无头模式（服务器部署设为 true）
concurrency: 1                    # 全局并发数（1-20）
port: 8080                        # Web 端口
```

## Zeabur 部署

### 1. Fork 仓库

Fork 本仓库到你的 GitHub 账号。

### 2. 创建项目

登录 [Zeabur](https://zeabur.com)，创建新项目，选择 **Link GitHub**，选择你 Fork 的仓库。

### 3. 等待构建

Zeabur 会自动识别 Dockerfile 并开始构建，首次构建需要几分钟（包含 Playwright Chromium 安装）。

### 4. 绑定域名

在服务设置中绑定域名，端口填 `8080`。

### 5. 挂载持久化磁盘

在服务的 **Disk** 选项中添加两个挂载：

| 挂载路径 | 用途 |
|---------|------|
| `/app/data` | 配置文件（config.yaml）和数据库（data.db） |
| `/app/auths` | OAuth 凭证文件 |

> 首次启动会自动从 `config.yaml.example` 生成默认配置，通过 Web 面板的设置页面修改即可。

### 6. 配置

通过绑定的域名访问 Web 面板，进入 **设置** 页面填写 CPA 和 SMS 平台信息。服务器部署建议开启 **无头模式**。

## 子号格式

导入子号时，支持以下格式（一行一个）：

```
# 三段式：邮箱---密码---2FA密钥
email@gmail.com---password---2fa_secret

# 四段式：邮箱---密码---辅助邮箱---2FA密钥
email@gmail.com---password---aux@email.com---2fa_secret
```

## 项目结构

```
├── main.go              # 入口
├── server/              # Web 服务器 + 静态前端
├── api/                 # CPA / SMS / OAuth 接口
├── automation/          # 自动化引擎（登录、家庭组、手机绑定）
├── config/              # 配置管理（热重载）
├── db/                  # SQLite 数据库
├── Dockerfile           # Docker 构建
├── config.yaml.example  # 配置模板
├── data/                # 运行时数据（config.yaml + data.db）
└── auths/               # OAuth 凭证文件
```

## 免责声明

本项目仅供学习和研究用途，不得用于任何商业或非法目的。使用本项目所产生的一切后果由使用者自行承担，与项目开发者无关。

- 本项目不提供任何形式的担保，包括但不限于适销性、特定用途适用性及非侵权性
- 使用者应遵守所在地区的法律法规以及相关平台的服务条款
- 开发者不对因使用本项目导致的任何直接或间接损失负责
- 如果本项目涉及的功能违反了相关服务条款，请使用者自行评估风险

下载或使用本项目即表示你已阅读并同意以上声明。
