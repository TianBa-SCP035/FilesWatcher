package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kardianos/service"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type FileScanner struct {
	sftpClient          *sftp.Client
	sshClient           *ssh.Client
	rootPath            string        // 要扫描的根目录
	targetPath          string        // 远程服务器目标路径
	scanInterval        time.Duration // 扫描间隔
	recordFile          string        // 记录已上传文件的JSON文件路径
	dirRecordFile       string        // 记录目录扫描信息的JSON文件路径
	targetFolder        string        // 目标文件夹名称，如reCall（保持向后兼容性）
	targetFolders       []string      // 目标文件夹名称列表
	targetFiles         []string      // 目标文件名列表
	stableChecks        int           // 连续多少次检查到大小不变
	stableInterval      time.Duration // 两次检查间隔
	uploadedFiles       map[string]time.Time // 记录已上传文件及其修改时间
	dirScanRecords      map[string]DirScanRecord // 记录目录扫描信息
	activeUploads       map[string]bool // 记录正在上传的文件
	wg                  sync.WaitGroup // 用于等待扫描goroutine完成
	closed              bool           // 标记stopChan是否已关闭
	mu                  sync.Mutex     // 保护closed标志
	stopChan            chan struct{} // 用于停止扫描的通道
	failedFiles         map[string]int // 记录上传失败文件及其重试次数
	failedLogFile       string        // 记录上传失败文件的日志文件路径
	maxRetryCount       int           // 最大重试次数
	sshHost             string        // SSH主机地址
	sshPort             int           // SSH端口
	sshUser             string        // SSH用户名
	sshPassword         string        // SSH密码
	uploadExistingFiles bool          // 是否上传已存在的文件
}

type UploadRecord struct {
	Path    string    `json:"path"`
	ModTime time.Time `json:"mod_time"`
}

type DirScanRecord struct {
	Path           string    `json:"path"`
	LastDirModTime time.Time `json:"last_dir_mod_time"`
}

// Config 结构体用于存储配置文件中的参数
type Config struct {
	Source           string
	Target           string
	Host             string
	User             string
	Password         string
	Interval         int
	TargetFolder     string // 保持向后兼容性
	TargetFile       string // 保持向后兼容性
	TargetFolders    []string
	TargetFiles      []string
	RecordFile       string
	StableChecks     int
	StableInterval   int
	DisableLog       bool   // 是否禁用日志输出
	MaxRetryCount    int    // 最大重试次数
	FailedLogFile    string // 失败文件记录日志路径
	UploadExistingFiles bool // 是否上传已存在的文件
}

// loadConfig 从配置文件中加载配置
func loadConfig(configPath string) (*Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("打开配置文件失败: %v", err)
	}
	defer file.Close()

	config := &Config{}
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过注释行和空行
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "source":
			// 如果source是"."，则使用配置文件所在目录
			if value == "." {
				configDir := filepath.Dir(configPath)
				config.Source = configDir
			} else {
				config.Source = value
			}
		case "target":
			config.Target = value
		case "host":
			config.Host = value
		case "user":
			config.User = value
		case "password":
			config.Password = value
		case "interval":
			interval, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("解析扫描间隔失败: %v", err)
			}
			config.Interval = interval
		case "target_folder":
			// 支持多个文件夹，用逗号分隔
			folders := strings.Split(value, ",")
			for i, folder := range folders {
				config.TargetFolders = append(config.TargetFolders, strings.TrimSpace(folder))
				if i == 0 {
					// 保持向后兼容性
					config.TargetFolder = strings.TrimSpace(folder)
				}
			}
		case "target_file":
			// 支持多个文件，用逗号分隔
			files := strings.Split(value, ",")
			for i, file := range files {
				config.TargetFiles = append(config.TargetFiles, strings.TrimSpace(file))
				if i == 0 {
					// 保持向后兼容性
					config.TargetFile = strings.TrimSpace(file)
				}
			}
		case "record_file":
			config.RecordFile = value
		case "stable_checks":
			if checks, err := strconv.Atoi(value); err == nil {
				config.StableChecks = checks
			}
		case "stable_interval":
			if interval, err := strconv.Atoi(value); err == nil {
				config.StableInterval = interval
			}
		case "disable_log":
			if strings.ToLower(value) == "true" || value == "1" {
				config.DisableLog = true
			} else if strings.ToLower(value) == "false" || value == "0" {
				config.DisableLog = false
			}
		case "max_retry_count":
			if count, err := strconv.Atoi(value); err == nil {
				config.MaxRetryCount = count
			}
		case "failed_log_file":
			config.FailedLogFile = value
		case "upload_existing_files":
			if strings.ToLower(value) == "true" || value == "1" {
				config.UploadExistingFiles = true
			} else if strings.ToLower(value) == "false" || value == "0" {
				config.UploadExistingFiles = false
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %v", err)
	}

	// 设置默认值
	if config.Interval == 0 {
		config.Interval = 10
	}
	if config.StableChecks == 0 {
		config.StableChecks = 3
	}
	if config.StableInterval == 0 {
		config.StableInterval = 1
	}
	if len(config.TargetFolders) == 0 {
		if config.TargetFolder == "" {
			config.TargetFolders = []string{"reCall"}
			config.TargetFolder = "reCall"
		} else {
			config.TargetFolders = []string{config.TargetFolder}
		}
	}
	if len(config.TargetFiles) == 0 {
		if config.TargetFile == "" {
			config.TargetFiles = []string{"2.csv"}
			config.TargetFile = "2.csv"
		} else {
			config.TargetFiles = []string{config.TargetFile}
		}
	}
	if config.RecordFile == "" {
		config.RecordFile = "uploaded.json"
	}
	if config.MaxRetryCount == 0 {
		config.MaxRetryCount = 2 // 默认重试2次
	}
	if config.FailedLogFile == "" {
		config.FailedLogFile = "Failed.txt" // 默认失败日志文件
	}
	// 设置默认值，如果没有配置upload_existing_files，默认为false（不上传已存在的文件）
	// 这样可以保持与之前版本的行为一致
	if !config.DisableLog {
		log.Printf("UploadExistingFiles配置: %v\n", config.UploadExistingFiles)
	}

	return config, nil
}

func NewFileScanner(configPath string) (*FileScanner, error) {
	// 获取程序所在目录
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("获取程序路径失败: %v", err)
	}
	exeDir := filepath.Dir(exePath)
	
	// 加载配置文件
	config, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("加载配置文件失败: %v", err)
	}

	// 确保记录文件和失败日志文件使用exe所在目录的绝对路径
	if !filepath.IsAbs(config.RecordFile) {
		config.RecordFile = filepath.Join(exeDir, config.RecordFile)
	}
	if !filepath.IsAbs(config.FailedLogFile) {
		config.FailedLogFile = filepath.Join(exeDir, config.FailedLogFile)
	}

	// 如果禁用日志，将日志输出重定向到空设备
	if config.DisableLog {
		// Windows下使用NUL，不考虑Linux下使用
		devNull, err := os.OpenFile("NUL", os.O_WRONLY, 0)
		if err != nil {
			return nil, fmt.Errorf("无法打开空设备: %v", err)
		}
		log.SetOutput(devNull)
	}

	// 解析SSH主机和端口
	host, port, err := parseSSHHost(config.Host)
	if err != nil {
		return nil, fmt.Errorf("解析SSH主机地址失败: %v", err)
	}

	// 配置SSH客户端
	sshConfig := &ssh.ClientConfig{
		User: config.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(config.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
		Config: ssh.Config{
			Ciphers: []string{"aes128-ctr", "aes192-ctr", "aes256-ctr", "aes128-gcm@openssh.com", "chacha20-poly1305@openssh.com"},
		},
	}

	// 添加调试信息
	log.Printf("尝试连接SSH服务器: %s:%d, 用户: %s\n", host, port, config.User)

	// 连接SSH服务器
	sshClient, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), sshConfig)
	if err != nil {
		return nil, fmt.Errorf("SSH连接失败: %v", err)
	}

	log.Printf("SSH连接成功！\n")

	// 创建SFTP客户端
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("SFTP客户端创建失败: %v", err)
	}

	log.Printf("SFTP客户端创建成功！\n")

	return &FileScanner{
		sftpClient:          sftpClient,
		sshClient:           sshClient,
		rootPath:            config.Source,
		targetPath:          config.Target,
		scanInterval:        time.Duration(config.Interval) * time.Minute,
		recordFile:          config.RecordFile,
		dirRecordFile:       strings.TrimSuffix(config.RecordFile, filepath.Ext(config.RecordFile)) + "_dirs.json",
		targetFolder:        config.TargetFolder,
	targetFolders:       config.TargetFolders,
		targetFiles:         config.TargetFiles,
		stableChecks:        config.StableChecks,
		stableInterval:      time.Duration(config.StableInterval) * time.Second,
		uploadedFiles:       make(map[string]time.Time),
		dirScanRecords:      make(map[string]DirScanRecord),
		activeUploads:       make(map[string]bool),
		stopChan:            make(chan struct{}),
		failedFiles:         make(map[string]int),
		failedLogFile:       config.FailedLogFile,
		maxRetryCount:       config.MaxRetryCount,
		sshHost:             host,
		sshPort:             port,
		sshUser:             config.User,
		sshPassword:         config.Password,
		uploadExistingFiles: config.UploadExistingFiles,
	}, nil
}

// parseSSHHost 解析SSH主机地址，返回主机和端口
func parseSSHHost(host string) (string, int, error) {
	parts := strings.Split(host, ":")
	if len(parts) == 1 {
		return parts[0], 22, nil
	}
	if len(parts) == 2 {
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, fmt.Errorf("无效的端口号: %v", err)
		}
		return parts[0], port, nil
	}
	return "", 0, fmt.Errorf("无效的主机地址格式")
}

// markExistingFilesAsUploaded 标记所有已存在的文件为已上传
// 当 uploadExistingFiles 设置为 false 时，在首次运行时调用此函数
func (fs *FileScanner) markExistingFilesAsUploaded() error {
	log.Printf("开始标记已存在的文件为已上传...\n")
	
	// 遍历根目录
	err := filepath.Walk(fs.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("访问路径 %s 失败: %v\n", path, err)
			return nil // 继续处理其他文件
		}

		// 如果是目录
		if info.IsDir() {
			// 检查是否是目标文件夹
			for _, folder := range fs.targetFolders {
				if info.Name() == folder {
					// 处理该目录下的目标文件
					if err := fs.markTargetDirFilesAsUploaded(path); err != nil {
						log.Printf("标记%s目录 %s 中的文件为已上传失败: %v\n", folder, path, err)
					}
					return filepath.SkipDir // 跳过此目录的子目录
				}
			}
		}

		return nil
	})
	
	if err != nil {
		return fmt.Errorf("遍历目录失败: %v", err)
	}
	
	// 保存上传记录
	if err := fs.saveUploadedRecords(); err != nil {
		return fmt.Errorf("保存上传记录失败: %v", err)
	}
	
	log.Printf("已标记 %d 个文件为已上传\n", len(fs.uploadedFiles))
	return nil
}

// markTargetDirFilesAsUploaded 标记目标目录中的文件为已上传
func (fs *FileScanner) markTargetDirFilesAsUploaded(targetDirPath string) error {
	// 读取目录中的所有文件
	files, err := os.ReadDir(targetDirPath)
	if err != nil {
		return fmt.Errorf("读取目录失败: %v", err)
	}
	
	// 对每个目标文件模式进行处理
	for _, targetFile := range fs.targetFiles {
		// 如果target_file设置为*，则匹配所有文件
		if targetFile == "*" {
			for _, file := range files {
				if !file.IsDir() {
					filePath := filepath.Join(targetDirPath, file.Name())
					fileInfo, err := file.Info()
					if err != nil {
						continue
					}
					// 标记文件为已上传
					fs.uploadedFiles[filePath] = fileInfo.ModTime()
					log.Printf("标记文件 %s 为已上传\n", filePath)
				}
			}
		} else {
			// 首先尝试精确匹配
			filePath := filepath.Join(targetDirPath, targetFile)
			if fileInfo, err := os.Stat(filePath); err == nil {
				// 标记文件为已上传
				fs.uploadedFiles[filePath] = fileInfo.ModTime()
				log.Printf("标记文件 %s 为已上传\n", filePath)
			} else {
				// 如果精确匹配失败，尝试后缀匹配（检查文件名是否以target_file结尾，忽略大小写）
				targetFileLower := strings.ToLower(targetFile)
				for _, file := range files {
					if !file.IsDir() && strings.HasSuffix(strings.ToLower(file.Name()), targetFileLower) {
						filePath := filepath.Join(targetDirPath, file.Name())
						fileInfo, err := file.Info()
						if err != nil {
							continue
						}
						// 标记文件为已上传
						fs.uploadedFiles[filePath] = fileInfo.ModTime()
						log.Printf("标记文件 %s 为已上传\n", filePath)
					}
				}
			}
		}
	}
	
	return nil
}

func (fs *FileScanner) Start() error {
	// 加载已上传文件记录
	if err := fs.loadUploadedRecords(); err != nil {
		return fmt.Errorf("加载上传记录失败: %v", err)
	}

	// 加载目录扫描记录
	if err := fs.loadDirScanRecords(); err != nil {
		return fmt.Errorf("加载目录扫描记录失败: %v", err)
	}

	// 加载失败文件记录
	if err := fs.loadFailedRecords(); err != nil {
		return fmt.Errorf("加载失败文件记录失败: %v", err)
	}

	// 如果配置为不上传已存在的文件，且是首次运行（上传记录为空），则标记所有已存在的文件为已上传
	if !fs.uploadExistingFiles && len(fs.uploadedFiles) == 0 {
		log.Printf("检测到首次运行且配置为不上传已存在的文件，开始标记已存在的文件...\n")
		if err := fs.markExistingFilesAsUploaded(); err != nil {
			return fmt.Errorf("标记已存在文件为已上传失败: %v", err)
		}
		log.Printf("已标记所有已存在的文件为已上传，后续将只上传新增或修改的文件\n")
	}

	// 确保目标目录存在
	if err := fs.checkAndReconnectSFTP(); err != nil {
		return fmt.Errorf("检查和重建SFTP连接失败: %v", err)
	}
	if err := fs.sftpClient.MkdirAll(fs.targetPath); err != nil {
		log.Printf("创建远程目录失败: %v\n", err)
	}

	// 启动扫描（确保一次扫描完成后再进行下一次，并且启动时先扫描再等待）
	fs.wg.Add(1)
	go func() {
		defer fs.wg.Done()
		// 定义扫描函数，避免代码重复
		doScan := func() {
			log.Printf("开始扫描 %s\n", fs.rootPath)
			if err := fs.scanAndUpload(); err != nil {
				log.Printf("扫描上传失败: %v\n", err)
			} else {
				log.Printf("扫描上传完成\n")
			}
		}
		
		// 首次运行：立即执行一次扫描
		doScan()
		
		// 后续运行：扫描完成后等待间隔时间，再执行下一次扫描
		for {
			select {
			case <-fs.stopChan:
				return
			default:
				// 等待扫描间隔时间
				time.Sleep(fs.scanInterval)
				
				// 检查是否应该停止
				select {
				case <-fs.stopChan:
					return
				default:
				}
				
				// 执行扫描
				doScan()
			}
		}
	}()

	return nil
}

func (fs *FileScanner) Stop() {
	// 防止重复关闭stopChan
	fs.mu.Lock()
	if !fs.closed {
		close(fs.stopChan)
		fs.closed = true
	}
	fs.mu.Unlock()
	
	// 等待扫描goroutine完成
	fs.wg.Wait()
}

func (fs *FileScanner) Close() error {
	// 保存目录扫描记录
	if err := fs.saveDirScanRecords(); err != nil {
		log.Printf("保存目录扫描记录失败: %v\n", err)
	}

	// 保存已上传文件记录
	if err := fs.saveUploadedRecords(); err != nil {
		log.Printf("保存上传记录失败: %v\n", err)
	}

	// 关闭SFTP客户端
	if fs.sftpClient != nil {
		fs.sftpClient.Close()
	}

	// 关闭SSH客户端
	if fs.sshClient != nil {
		fs.sshClient.Close()
	}

	return nil
}



func (fs *FileScanner) scanAndUpload() error {
	return filepath.Walk(fs.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("访问路径 %s 失败: %v\n", path, err)
			return nil // 继续处理其他文件
		}

		// 如果是目录
		if info.IsDir() {
			// 检查目录是否需要扫描
			if fs.shouldScanDir(path, info.ModTime()) {
				// 检查是否是目标文件夹
				for _, folder := range fs.targetFolders {
					if info.Name() == folder {
						log.Printf("找到%s目录: %s\n", folder, path)
						// 处理该目录下的目标文件
						if err := fs.processTargetDir(path); err != nil {
							log.Printf("处理%s目录 %s 失败: %v\n", folder, path, err)
						}
						// 更新目录扫描记录
				fs.updateDirScanRecord(path, info.ModTime())
				// 每次扫描完成后都保存目录扫描记录
				if err := fs.saveDirScanRecords(); err != nil {
					log.Printf("保存目录扫描记录失败: %v\n", err)
				}
						return filepath.SkipDir // 跳过此目录的子目录
					}
				}
				// 非目标文件夹，更新目录扫描记录
				fs.updateDirScanRecord(path, info.ModTime())
			} else {
				// 如果目录不需要扫描，跳过其子目录
				return filepath.SkipDir
			}
		}

		return nil
	})
}



// needsUpload 检查文件是否需要上传（未上传或已修改）
func (fs *FileScanner) needsUpload(filePath string, fileInfo os.FileInfo) bool {
	// 检查是否已上传且未修改
	if uploadTime, exists := fs.uploadedFiles[filePath]; exists {
		if uploadTime.Equal(fileInfo.ModTime()) {
			return false // 文件已上传且未修改，不需要上传
		} else {
			return true // 文件已上传但已修改，需要重新上传
		}
	} else {
		// 文件未上传，检查是否在失败记录中且已达到最大重试次数
		if retryCount, exists := fs.failedFiles[filePath]; exists && retryCount >= fs.maxRetryCount {
			return false // 文件未上传但已达到最大重试次数，不需要上传
		}
		
		return true // 文件未上传，需要上传
	}
}

// processTargetDir 处理目标目录中的文件（优化版，批量检查文件稳定性）
func (fs *FileScanner) processTargetDir(targetDirPath string) error {
	log.Printf("开始处理目标文件夹: %s\n", targetDirPath)
	
	// 循环扫描，直到没有新文件需要上传
	for {
		// 第一阶段：扫描所有需要上传的文件（不检查稳定性）
		filesToCheck := make(map[string]string)
		if err := fs.collectFilesForUpload(targetDirPath, filesToCheck); err != nil {
			return err
		}
		
		// 如果没有文件需要上传，退出循环
		if len(filesToCheck) == 0 {
			log.Printf("目标文件夹 %s 中没有需要上传的文件，扫描完成\n", targetDirPath)
			break
		}
		
		log.Printf("目标文件夹 %s 中发现 %d 个文件需要检查稳定性\n", targetDirPath, len(filesToCheck))
		
		// 第二阶段：批量检查文件稳定性
		stableFiles := make(map[string]string)
		unstableFiles := make(map[string]string)
		
		if err := fs.batchCheckStability(filesToCheck, stableFiles, unstableFiles); err != nil {
			return err
		}
		
		log.Printf("稳定性检查完成: %d 个文件稳定，%d 个文件不稳定\n", len(stableFiles), len(unstableFiles))
		
		// 第三阶段：上传已稳定的文件
		if len(stableFiles) > 0 {
			log.Printf("开始上传 %d 个已稳定文件\n", len(stableFiles))
			successCount := 0
			failCount := 0
			
			// 遍历所有待上传文件
			for filePath := range stableFiles {
				// 检查文件是否已经在上传中
				if _, isUploading := fs.activeUploads[filePath]; isUploading {
					continue
				}
				
				// 标记文件为上传中
				fs.activeUploads[filePath] = true
				
				// 处理文件
				if err := fs.processFile(filePath); err != nil {
					failCount++
				} else {
					successCount++
				}
				
				// 从上传中列表移除
				delete(fs.activeUploads, filePath)
			}
			
			log.Printf("上传完成: 成功 %d 个文件，失败 %d 个文件\n", successCount, failCount)
			
			// 如果有文件上传失败，返回错误
			if failCount > 0 {
				return fmt.Errorf("有 %d 个文件上传失败", failCount)
			}
		}
		
		// 如果没有不稳定的文件，结束处理
		if len(unstableFiles) == 0 {
			log.Printf("目标文件夹 %s 所有文件已稳定并上传完成\n", targetDirPath)
			
			// 第五阶段：再次检查文件夹，确保没有新添加的文件
			log.Printf("再次检查文件夹 %s，确保没有新添加的文件\n", targetDirPath)
			newFilesToCheck := make(map[string]string)
			if err := fs.collectFilesForUpload(targetDirPath, newFilesToCheck); err != nil {
				return err
			}
			
			// 如果有新文件需要上传，继续处理
			if len(newFilesToCheck) > 0 {
				log.Printf("发现 %d 个新文件需要上传，继续处理\n", len(newFilesToCheck))
				continue // 继续下一轮循环，处理新文件
			}
			
			break // 没有新文件，结束处理
		}
		
		// 第四阶段：等待一段时间，让未稳定文件完成写入
		log.Printf("等待 %d 个未稳定文件完成写入...\n", len(unstableFiles))
		time.Sleep(5 * time.Second)
	}
	
	log.Printf("目标文件夹 %s 处理完成\n", targetDirPath)
	return nil
}

// collectFilesForUpload 收集需要上传的文件（优化版，先匹配文件再检查是否需要上传）
func (fs *FileScanner) collectFilesForUpload(targetDirPath string, filesToCheck map[string]string) error {
	// 读取目录中的所有文件
	files, err := os.ReadDir(targetDirPath)
	if err != nil {
		return fmt.Errorf("读取目录失败: %v", err)
	}
	
	// 对每个目标文件模式进行处理
	for _, targetFile := range fs.targetFiles {
		// 第一阶段：寻找匹配的文件
		matchedFiles := make([]string, 0)
		
		// 如果target_file设置为*，则匹配所有文件
		if targetFile == "*" {
			for _, file := range files {
				if !file.IsDir() {
					filePath := filepath.Join(targetDirPath, file.Name())
					matchedFiles = append(matchedFiles, filePath)
				}
			}
		} else {
			// 首先尝试精确匹配
			filePath := filepath.Join(targetDirPath, targetFile)
			if _, err := os.Stat(filePath); err == nil {
				matchedFiles = append(matchedFiles, filePath)
			} else {
				// 如果精确匹配失败，尝试后缀匹配（检查文件名是否以target_file结尾，忽略大小写）
				targetFileLower := strings.ToLower(targetFile)
				for _, file := range files {
					if !file.IsDir() && strings.HasSuffix(strings.ToLower(file.Name()), targetFileLower) {
						filePath := filepath.Join(targetDirPath, file.Name())
						matchedFiles = append(matchedFiles, filePath)
					}
				}
			}
		}
		
		// 第二阶段：检查匹配的文件是否需要上传
		for _, filePath := range matchedFiles {
			// 获取文件信息
			fileInfo, err := os.Stat(filePath)
			if err != nil {
				continue
			}
			
			// 检查文件是否需要上传（未上传或已修改）
			if fs.needsUpload(filePath, fileInfo) {
				filesToCheck[filePath] = targetDirPath
			}
		}
	}
	
	return nil
}

// batchCheckStability 批量检查文件稳定性
func (fs *FileScanner) batchCheckStability(filesToCheck map[string]string, stableFiles map[string]string, unstableFiles map[string]string) error {
	// 记录每个文件的大小变化
	fileSizes := make(map[string][]int64)
	
	// 使用配置的检查次数和间隔时间，记录文件大小
	for i := 0; i < fs.stableChecks; i++ {
		if i > 0 {
			time.Sleep(fs.stableInterval)
		}
		
		for filePath := range filesToCheck {
			size, err := fs.fileSize(filePath)
			if err != nil {
				continue
			}
			
			if fileSizes[filePath] == nil {
				fileSizes[filePath] = make([]int64, 0, fs.stableChecks)
			}
			fileSizes[filePath] = append(fileSizes[filePath], size)
		}
	}
	
	// 分析文件大小变化，判断文件是否稳定
	for filePath, sizes := range fileSizes {
		if len(sizes) < fs.stableChecks {
			// 如果没有完整的记录，认为文件不稳定
			unstableFiles[filePath] = filesToCheck[filePath]
			continue
		}
		
		// 检查所有记录的大小是否相同
		isStable := true
		for i := 1; i < len(sizes); i++ {
			if sizes[i] != sizes[0] {
				isStable = false
				break
			}
		}
		
		if isStable {
			stableFiles[filePath] = filesToCheck[filePath]
		} else {
			unstableFiles[filePath] = filesToCheck[filePath]
		}
	}
	
	return nil
}



// processFile 处理单个文件的上传（优化版，无需再检查文件稳定性）
func (fs *FileScanner) processFile(filePath string) error {
	// 获取文件信息
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("获取文件信息失败: %v", err)
	}

	// 检查是否已上传且未修改
	if uploadTime, exists := fs.uploadedFiles[filePath]; exists && uploadTime.Equal(fileInfo.ModTime()) {
		return nil // 文件已上传且未修改，跳过
	}

	// 检查是否在失败记录中且已达到最大尝试次数
	if retryCount, exists := fs.failedFiles[filePath]; exists && retryCount >= fs.maxRetryCount {
		log.Printf("文件 %s 已达到最大尝试次数 %d，跳过上传\n", filePath, fs.maxRetryCount)
		return nil
	}

	// 重新获取文件信息，因为等待过程中文件可能已修改
	fileInfo, err = os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("重新获取文件信息失败: %v", err)
	}

	// 获取当前重试次数，如果不存在则为0
	// retryCount := 0
	// if count, exists := fs.failedFiles[filePath]; exists {
	// 	retryCount = count
	// }

	// 尝试上传文件，最多尝试maxRetryCount次
	for attempt := 1; attempt <= fs.maxRetryCount; attempt++ {
		if attempt > 1 {
			log.Printf("文件 %s 上传失败，进行第 %d 次尝试\n", filePath, attempt)
			// 等待一段时间再重试
			time.Sleep(time.Duration(attempt-1) * time.Second)
		} else {
			log.Printf("开始上传文件 %s (第 %d 次尝试)\n", filePath, attempt)
		}

		// 上传文件
		if err := fs.uploadFile(filePath); err != nil {
			if attempt == fs.maxRetryCount {
				// 所有尝试都失败，记录到失败文件
				if logErr := fs.logFailedFile(filePath, attempt); logErr != nil {
					log.Printf("记录失败文件失败: %v\n", logErr)
				}
				return fmt.Errorf("文件 %s 上传失败，已尝试 %d 次: %v", filePath, fs.maxRetryCount, err)
			}
			continue
		}

		// 上传成功，更新上传记录
		fs.uploadedFiles[filePath] = fileInfo.ModTime()
		if err := fs.saveUploadedRecords(); err != nil {
			log.Printf("保存上传记录失败: %v\n", err)
		}

		// 如果之前有失败记录，从失败记录中删除
		if _, exists := fs.failedFiles[filePath]; exists {
			delete(fs.failedFiles, filePath)
		}

		return nil
	}

	return fmt.Errorf("文件 %s 上传失败，已尝试 %d 次", filePath, fs.maxRetryCount)
}

// checkAndReconnectSFTP 检查SFTP连接是否有效，如果无效则重新连接
func (fs *FileScanner) checkAndReconnectSFTP() error {
	// 尝试执行一个简单的操作来检查连接是否有效
	_, err := fs.sftpClient.Getwd()
	if err == nil {
		// 连接正常
		return nil
	}

	log.Printf("SFTP连接已断开，尝试重新连接...\n")

	// 关闭旧的连接
	if fs.sftpClient != nil {
		fs.sftpClient.Close()
	}
	if fs.sshClient != nil {
		fs.sshClient.Close()
	}

	// 配置SSH客户端
	sshConfig := &ssh.ClientConfig{
		User: fs.sshUser,
		Auth: []ssh.AuthMethod{
			ssh.Password(fs.sshPassword),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
		Config: ssh.Config{
			Ciphers: []string{"aes128-ctr", "aes192-ctr", "aes256-ctr", "aes128-gcm@openssh.com", "chacha20-poly1305@openssh.com"},
		},
	}

	// 连接SSH服务器，直接使用保存的主机和端口
	sshClient, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", fs.sshHost, fs.sshPort), sshConfig)
	if err != nil {
		return fmt.Errorf("SSH连接失败: %v", err)
	}

	// 创建SFTP客户端
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return fmt.Errorf("SFTP客户端创建失败: %v", err)
	}

	// 更新连接
	fs.sshClient = sshClient
	fs.sftpClient = sftpClient

	log.Printf("SFTP连接重新建立成功！\n")
	return nil
}

func (fs *FileScanner) uploadFile(filePath string) error {
	// 检查并重建SFTP连接
	if err := fs.checkAndReconnectSFTP(); err != nil {
		return fmt.Errorf("检查和重建SFTP连接失败: %v", err)
	}

	// 打开源文件
	sourceFile, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("打开源文件失败: %v", err)
	}
	defer sourceFile.Close()

	// 获取目标文件名
	targetFileName := filepath.Base(filePath)
	finalFilePath := filepath.Join(fs.targetPath, targetFileName)
	finalFilePath = filepath.ToSlash(finalFilePath) // 确保使用Linux风格的路径分隔符

	// 检查远程文件是否已存在
	if _, err := fs.sftpClient.Stat(finalFilePath); err == nil {
		// 文件已存在，先删除
		if err := fs.sftpClient.Remove(finalFilePath); err != nil {
			return fmt.Errorf("删除已存在的远程文件失败: %v", err)
		}
	}

	// 创建临时文件
	tempFilePath := filepath.Join(fs.targetPath, targetFileName+".part")
	tempFilePath = filepath.ToSlash(tempFilePath) // 确保使用Linux风格的路径分隔符
	tempFile, err := fs.sftpClient.Create(tempFilePath)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %v", err)
	}

	// 复制文件内容
	buf := make([]byte, 1024*1024) // 1MB buffer
	defer tempFile.Close() // 确保文件最终会被关闭
	for {
		n, err := sourceFile.Read(buf)
		if n > 0 {
			if _, werr := tempFile.Write(buf[:n]); werr != nil {
				return fmt.Errorf("写入临时文件失败: %w", werr)
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("读取源文件失败: %w", err)
		}
	}

	// 重命名为最终文件名
	if err := fs.sftpClient.Rename(tempFilePath, finalFilePath); err != nil {
		return fmt.Errorf("重命名文件失败: %v", err)
	}

	return nil
}

func (fs *FileScanner) loadUploadedRecords() error {
	fs.uploadedFiles = make(map[string]time.Time)

	// 如果记录文件不存在，直接返回
	if _, err := os.Stat(fs.recordFile); os.IsNotExist(err) {
		log.Printf("记录文件 %s 不存在，将创建新文件\n", fs.recordFile)
		return nil
	}

	// 读取记录文件
	data, err := os.ReadFile(fs.recordFile)
	if err != nil {
		return fmt.Errorf("读取记录文件失败: %v", err)
	}

	// 解析JSON
	var records []UploadRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("解析记录文件失败: %v", err)
	}

	// 填充uploadedFiles
	for _, r := range records {
		fs.uploadedFiles[r.Path] = r.ModTime
	}

	log.Printf("成功加载 %d 条上传记录\n", len(records))
	return nil
}

func (fs *FileScanner) saveUploadedRecords() error {
	// 准备数据
	var records []UploadRecord
	for path, modTime := range fs.uploadedFiles {
		records = append(records, UploadRecord{
			Path:    path,
			ModTime: modTime,
		})
	}

	// 序列化为JSON
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化记录失败: %v", err)
	}

	// 使用原子写入方式：先写入临时文件，再重命名
	tmpFile := fs.recordFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("写入临时记录文件失败: %v", err)
	}
	
	// 原子重命名，确保要么保留老文件，要么整体替换为新文件
	if err := os.Rename(tmpFile, fs.recordFile); err != nil {
		return fmt.Errorf("重命名临时记录文件失败: %v", err)
	}

	log.Printf("成功保存 %d 条上传记录\n", len(records))
	return nil
}

// shouldScanDir 检查目录是否需要扫描（基于mtime优化）
func (fs *FileScanner) shouldScanDir(dirPath string, dirModTime time.Time) bool {
	// 如果是根目录，总是需要扫描
	if dirPath == fs.rootPath {
		return true
	}

	// 检查是否是目标文件夹
	dirName := filepath.Base(dirPath)
	for _, targetFolder := range fs.targetFolders {
		if dirName == targetFolder {
			// 目标文件夹总是需要扫描，不受mtime限制
			return true
		}
	}

	// 检查目录扫描记录
	record, exists := fs.dirScanRecords[dirPath]
	if !exists {
		// 如果没有记录，需要扫描
		return true
	}

	// 如果目录修改时间晚于记录的修改时间，需要扫描
	if dirModTime.After(record.LastDirModTime) {
		return true
	}

	// 检查子目录是否有更新
	return fs.hasSubdirUpdated(dirPath, record.LastDirModTime)
}

// hasSubdirUpdated 检查目录是否有子目录在指定时间后被更新
// 注意：深层文件修改不会更新父目录的mtime，因此需要递归检查每个子目录
func (fs *FileScanner) hasSubdirUpdated(dirPath string, lastModTime time.Time) bool {
	// 读取目录内容
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false // 读取失败，假设没有更新
	}

	// 检查每个子目录
	for _, entry := range entries {
		if entry.IsDir() {
			subDirPath := filepath.Join(dirPath, entry.Name())
			
			// 获取子目录信息
			info, err := entry.Info()
			if err != nil {
				continue // 获取失败，跳过
			}
			
			// 如果子目录修改时间晚于上次检查时间，需要扫描
			if info.ModTime().After(lastModTime) {
				return true
			}
			
			// 递归检查子目录的子目录
			// 必须递归检查，因为深层文件修改不会更新父目录的mtime
			if fs.hasSubdirUpdated(subDirPath, lastModTime) {
				return true
			}
		}
	}

	return false
}

// updateDirScanRecord 更新目录扫描记录
func (fs *FileScanner) updateDirScanRecord(dirPath string, dirModTime time.Time) {
	fs.dirScanRecords[dirPath] = DirScanRecord{
		Path:           dirPath,
		LastDirModTime: dirModTime,
	}
}

// loadDirScanRecords 加载目录扫描记录
func (fs *FileScanner) loadDirScanRecords() error {
	data, err := os.ReadFile(fs.dirRecordFile)
	if err != nil {
		if os.IsNotExist(err) {
			// 如果文件不存在，说明是第一次运行，没有错误
			return nil
		}
		return err
	}

	var records []DirScanRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return err
	}

	// 将记录转换为map方便查找
	fs.dirScanRecords = make(map[string]DirScanRecord)
	for _, record := range records {
		fs.dirScanRecords[record.Path] = record
	}

	return nil
}

// saveDirScanRecords 保存目录扫描记录
func (fs *FileScanner) saveDirScanRecords() error {
	// 将map转换为slice
	var records []DirScanRecord
	for _, record := range fs.dirScanRecords {
		records = append(records, record)
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}

	// 使用原子写入方式：先写入临时文件，再重命名
	tmpFile := fs.dirRecordFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("写入临时目录记录文件失败: %v", err)
	}
	
	// 原子重命名，确保要么保留老文件，要么整体替换为新文件
	return os.Rename(tmpFile, fs.dirRecordFile)
}

// loadFailedRecords 加载失败文件记录
func (fs *FileScanner) loadFailedRecords() error {
	fs.failedFiles = make(map[string]int)

	// 如果记录文件不存在，直接返回
	if _, err := os.Stat(fs.failedLogFile); os.IsNotExist(err) {
		log.Printf("失败记录文件 %s 不存在\n", fs.failedLogFile)
		return nil
	}

	// 读取记录文件
	file, err := os.Open(fs.failedLogFile)
	if err != nil {
		return fmt.Errorf("打开失败记录文件失败: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// 解析格式：日期 时间 文件路径
		parts := strings.SplitN(line, " ", 3) // 使用SplitN并限制分割为3部分，确保正确提取日期、时间和文件路径
		if len(parts) >= 3 {
			// 第一个部分是日期，第二个部分是时间，第三个部分是文件路径
			filePath := parts[2]
			
			// 将文件路径添加到失败记录中，标记为已达到最大尝试次数
			fs.failedFiles[filePath] = fs.maxRetryCount
		}
	}

	return nil
}

// logFailedFile 记录上传失败的文件
func (fs *FileScanner) logFailedFile(filePath string, attemptCount int) error {
	// 更新内存中的记录
	fs.failedFiles[filePath] = attemptCount

	// 打开文件（追加模式）
	file, err := os.OpenFile(fs.failedLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		return fmt.Errorf("打开失败记录文件失败: %v", err)
	}
	defer file.Close()

	// 写入记录，格式：时间（到分钟） 文件路径
	timeStr := time.Now().Format("2006-01-02 15:04")
	record := fmt.Sprintf("%s %s\n", timeStr, filePath)
	if _, err := file.WriteString(record); err != nil {
		return fmt.Errorf("写入失败记录失败: %v", err)
	}
	
	// 确保数据写入磁盘
	if err := file.Sync(); err != nil {
		return fmt.Errorf("同步失败记录文件到磁盘失败: %v", err)
	}

	return nil
}





// fileSize 获取文件大小
func (fs *FileScanner) fileSize(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("is directory")
	}
	return info.Size(), nil
}

// program 实现service.Interface接口
type program struct {
	scanner *FileScanner
}

func (p *program) Start(s service.Service) error {
	go p.run()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.scanner != nil {
		p.scanner.Stop()
		p.scanner.Close()
	}
	return nil
}

func (p *program) run() {
	// 获取程序所在目录
	exePath, err := os.Executable()
	if err != nil {
		log.Printf("获取程序路径失败: %v", err)
		return
	}
	exeDir := filepath.Dir(exePath)
	
	// 获取配置文件路径
	configPath := filepath.Join(exeDir, "config.txt")
	if len(os.Args) > 1 && os.Args[1] != "install" && os.Args[1] != "uninstall" {
		configPath = os.Args[1]
	}

	// 切换工作目录到程序所在目录
	if err := os.Chdir(exeDir); err != nil {
		log.Printf("切换工作目录失败: %v", err)
		return
	}

	// 创建文件扫描器
	scanner, err := NewFileScanner(configPath)
	if err != nil {
		log.Printf("创建文件扫描器失败: %v", err)
		return
	}
	p.scanner = scanner

	// 启动扫描
	if err := scanner.Start(); err != nil {
		log.Printf("启动扫描失败: %v", err)
		return
	}

	log.Println("文件扫描器已启动")
}

func main() {
	svcConfig := &service.Config{
		Name:        "FileScannerService",
		DisplayName: "File Scanner Service",
		Description: "扫描文件夹并自动上传文件到远程服务器",
	}

	prg := &program{}
	s, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatal(err)
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			err = s.Install()
			if err != nil {
				log.Fatal(err)
			}
			log.Println("服务已安装")
			return
		case "uninstall", "remove":
			err = s.Uninstall()
			if err != nil {
				log.Fatal(err)
			}
			log.Println("服务已卸载")
			return
		}
	}

	err = s.Run()
	if err != nil {
		log.Fatal(err)
	}
}