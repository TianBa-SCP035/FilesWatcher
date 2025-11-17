# FilesWatcher 📁

一个高效可靠的文件监控与自动上传工具，专为需要实时同步文件到远程服务器的场景设计。

## ✨ 主要功能

- **智能文件监控**：持续监控指定目录下的特定文件夹和文件
- **自动上传**：发现新文件或修改的文件后自动通过SFTP上传到远程服务器
- **文件稳定性检查**：确保文件完全写入后再上传，避免上传不完整文件
- **失败重试机制**：上传失败时自动重试，提高可靠性
- **上传记录追踪**：记录已上传文件，避免重复上传
- **灵活配置**：支持多种配置选项，适应不同使用场景

## 🚀 快速开始

### 环境要求

- Go 1.16 或更高版本
- 支持SFTP的远程服务器

### 安装

1. 克隆仓库
```bash
git clone https://github.com/TianBa-SCP035/FilesWatcher.git
cd FilesWatcher
```

2. 构建项目
```bash
go build -o FilesWatcher.exe
```

3. 配置参数
编辑 `config.txt` 文件，设置您的监控目录和远程服务器信息

4. 运行程序
```bash
./FilesWatcher.exe
```

## ⚙️ 配置说明

在 `config.txt` 文件中配置以下参数：

| 参数 | 说明 | 默认值 |
|------|------|--------|
| source | 本地监控目录 | - |
| target | 远程服务器目录 | - |
| host | SSH服务器地址 (格式: IP:端口) | - |
| user | SSH用户名 | - |
| password | SSH密码 | - |
| interval | 扫描间隔(分钟) | 10 |
| target_folder | 目标文件夹名称，多个用逗号分隔 | reCall |
| target_file | 目标文件后缀名，多个用逗号分隔 | 2.csv |
| record_file | 上传记录文件名 | uploaded.json |
| stable_checks | 连续检查到相同大小的次数 | 3 |
| stable_interval | 检查间隔(秒) | 1 |
| disable_log | 是否禁用日志输出 (true/false) | false |
| max_retry_count | 最大重试次数 | 2 |
| upload_existing_files | 首次运行是否上传已存在的文件 | false |

## 📋 使用示例

假设您需要监控 `D:\data` 目录下的 `reCall` 文件夹中的所有 `.csv` 文件，并将它们上传到远程服务器：

```ini
# 本地监控目录
source=D:\data
# 远程服务器目录
target=/remote/path/

# SSH服务器地址
host=192.168.1.100:22
# SSH用户名
user=username
# SSH密码
password=password

# 扫描间隔(分钟)
interval=5
# 目标文件夹名称
target_folder=reCall
# 目标文件后缀名
target_file=*.csv
```

## 🔧 工作原理

1. **扫描阶段**：程序按照配置的间隔扫描指定目录
2. **检测阶段**：查找匹配目标文件夹和文件模式的文件
3. **稳定性检查**：对发现的文件进行多次大小检查，确保文件已完全写入
4. **上传阶段**：通过SFTP将稳定的文件上传到远程服务器
5. **记录阶段**：记录已上传文件信息，避免重复上传

## 🛠️ 技术栈

- **语言**：Go
- **主要依赖**：
  - golang.org/x/crypto/ssh - SSH连接
  - github.com/pkg/sftp - SFTP文件传输
  - github.com/kardianos/service - Windows服务支持

## 📝 许可证

本项目采用 [Apache License 2.0](LICENSE) 许可证。

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

---

**注意**：请确保您的SSH连接信息安全，避免在生产环境中使用明文密码。建议使用SSH密钥认证方式提高安全性。