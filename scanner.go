package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// ScannerConfig 定义扫描器的配置参数
type ScannerConfig struct {
	RootPath      string        // 要扫描的根目录
	TargetPattern string        // 目标文件夹名称模式，如"reCall"
	FilePattern   string        // 目标文件名模式，如"2.csv"
	ScanInterval  time.Duration // 扫描间隔
	RecordFile    string        // 记录已上传文件的JSON文件路径
}

// NewScannerConfig 创建默认的扫描器配置
func NewScannerConfig() *ScannerConfig {
	return &ScannerConfig{
		RootPath:      ".",
		TargetPattern: "reCall",
		FilePattern:   "2.csv",
		ScanInterval:  30 * time.Second,
		RecordFile:    "uploaded.json",
	}
}

// Validate 验证配置参数是否有效
func (c *ScannerConfig) Validate() error {
	if c.RootPath == "" {
		return fmt.Errorf("根目录不能为空")
	}
	if c.TargetPattern == "" {
		return fmt.Errorf("目标文件夹名称模式不能为空")
	}
	if c.FilePattern == "" {
		return fmt.Errorf("目标文件名模式不能为空")
	}
	if c.ScanInterval <= 0 {
		return fmt.Errorf("扫描间隔必须大于0")
	}
	if c.RecordFile == "" {
		return fmt.Errorf("记录文件路径不能为空")
	}
	return nil
}

// ScanResult 扫描结果
type ScanResult struct {
	FilePath string    // 文件路径
	ModTime  time.Time // 文件修改时间
	Size     int64     // 文件大小
}

// ScanFiles 扫描指定目录下所有匹配的文件
func (fs *FileScanner) ScanFiles() ([]ScanResult, error) {
	var results []ScanResult

	// 遍历根目录
	err := filepath.Walk(fs.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("访问路径 %s 失败: %v\n", path, err)
			return nil // 继续处理其他文件
		}

		// 如果是目录且名称匹配目标模式
		if info.IsDir() && info.Name() == fs.targetFolder {
			log.Printf("找到%s目录: %s\n", fs.targetFolder, path)
			// 处理该目录下的目标文件
			if err := fs.processTargetDir(path); err != nil {
				log.Printf("处理%s目录 %s 失败: %v\n", fs.targetFolder, path, err)
			}
			return filepath.SkipDir // 跳过此目录的子目录
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("扫描目录失败: %v", err)
	}

	return results, nil
}

// IsFileUploaded 检查文件是否已上传且未修改
func (fs *FileScanner) IsFileUploaded(filePath string, modTime time.Time) bool {
	if uploadTime, exists := fs.uploadedFiles[filePath]; exists {
		return uploadTime.Equal(modTime)
	}
	return false
}

// MarkFileUploaded 标记文件为已上传
func (fs *FileScanner) MarkFileUploaded(filePath string, modTime time.Time) error {
	fs.uploadedFiles[filePath] = modTime
	return fs.saveUploadedRecords()
}

// GetUploadedFilesCount 获取已上传文件数量
func (fs *FileScanner) GetUploadedFilesCount() int {
	return len(fs.uploadedFiles)
}

// ClearUploadedRecords 清空上传记录
func (fs *FileScanner) ClearUploadedRecords() error {
	fs.uploadedFiles = make(map[string]time.Time)
	return fs.saveUploadedRecords()
}

// GetFileModTime 获取文件修改时间
func GetFileModTime(filePath string) (time.Time, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return time.Time{}, err
	}
	return fileInfo.ModTime(), nil
}

// GetFileSize 获取文件大小
func GetFileSize(filePath string) (int64, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return 0, err
	}
	return fileInfo.Size(), nil
}

// FileExists 检查文件是否存在
func FileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return !os.IsNotExist(err)
}

// EnsureDirExists 确保目录存在
func EnsureDirExists(dirPath string) error {
	return os.MkdirAll(dirPath, 0755)
}