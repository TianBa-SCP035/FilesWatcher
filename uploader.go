package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// UploadConfig 定义上传器的配置参数
type UploadConfig struct {
	SSHHost     string // SSH服务器地址
	SSHPort     int    // SSH端口
	SSHUser     string // SSH用户名
	SSHPassword string // SSH密码
	TargetPath  string // 远程服务器目标路径
}

// NewUploadConfig 创建默认的上传器配置
func NewUploadConfig() *UploadConfig {
	return &UploadConfig{
		SSHPort:    22,
		TargetPath: "/remote/path",
	}
}

// Validate 验证配置参数是否有效
func (c *UploadConfig) Validate() error {
	if c.SSHHost == "" {
		return fmt.Errorf("SSH服务器地址不能为空")
	}
	if c.SSHPort <= 0 || c.SSHPort > 65535 {
		return fmt.Errorf("SSH端口必须在1-65535之间")
	}
	if c.SSHUser == "" {
		return fmt.Errorf("SSH用户名不能为空")
	}
	if c.SSHPassword == "" {
		return fmt.Errorf("SSH密码不能为空")
	}
	if c.TargetPath == "" {
		return fmt.Errorf("目标路径不能为空")
	}
	return nil
}

// UploadResult 上传结果
type UploadResult struct {
	Success      bool      // 是否成功
	FilePath     string    // 本地文件路径
	RemotePath   string    // 远程文件路径
	Size         int64     // 文件大小
	Duration     time.Duration // 上传耗时
	ErrorMessage string    // 错误信息
}

// NewSFTPClient 创建新的SFTP客户端
func NewSFTPClient(host string, port int, user, password string) (*sftp.Client, *ssh.Client, error) {
	// 配置SSH客户端
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
		Config: ssh.Config{
			Ciphers: []string{"aes128-ctr", "aes192-ctr", "aes256-ctr", "aes128-gcm@openssh.com", "chacha20-poly1305@openssh.com"},
		},
	}

	// 添加调试信息
	log.Printf("尝试连接SSH服务器: %s:%d, 用户: %s\n", host, port, user)

	// 连接SSH服务器
	sshClient, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
	if err != nil {
		return nil, nil, fmt.Errorf("SSH连接失败: %v", err)
	}

	log.Printf("SSH连接成功！\n")

	// 创建SFTP客户端
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, nil, fmt.Errorf("SFTP客户端创建失败: %v", err)
	}

	log.Printf("SFTP客户端创建成功！\n")

	return sftpClient, sshClient, nil
}

// UploadFile 上传文件到远程服务器
func (fs *FileScanner) UploadFile(filePath string) (*UploadResult, error) {
	startTime := time.Now()
	result := &UploadResult{
		FilePath: filePath,
	}

	// 获取文件信息
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("获取文件信息失败: %v", err)
		return result, err
	}
	result.Size = fileInfo.Size()

	// 打开源文件
	sourceFile, err := os.Open(filePath)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("打开源文件失败: %v", err)
		return result, err
	}
	defer sourceFile.Close()

	// 创建临时文件
	fileName := filepath.Base(filePath)
	tempFilePath := filepath.Join(fs.targetPath, fileName+".part")
	tempFilePath = filepath.ToSlash(tempFilePath) // 确保使用Linux风格的路径分隔符
	tempFile, err := fs.sftpClient.Create(tempFilePath)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("创建临时文件失败: %v", err)
		return result, err
	}

	// 复制文件内容
	buf := make([]byte, 1024*1024) // 1MB buffer
	for {
		n, err := sourceFile.Read(buf)
		if err != nil {
			tempFile.Close()
			if err.Error() == "EOF" {
				break
			}
			result.ErrorMessage = fmt.Sprintf("读取源文件失败: %v", err)
			return result, err
		}
		if n == 0 {
			break
		}
		if _, err := tempFile.Write(buf[:n]); err != nil {
			tempFile.Close()
			result.ErrorMessage = fmt.Sprintf("写入临时文件失败: %v", err)
			return result, err
		}
	}
	tempFile.Close()

	// 重命名为最终文件名
	finalFilePath := filepath.Join(fs.targetPath, fileName)
	finalFilePath = filepath.ToSlash(finalFilePath)
	if err := fs.sftpClient.Rename(tempFilePath, finalFilePath); err != nil {
		result.ErrorMessage = fmt.Sprintf("重命名文件失败: %v", err)
		return result, err
	}

	// 设置结果
	result.Success = true
	result.RemotePath = finalFilePath
	result.Duration = time.Since(startTime)

	log.Printf("文件 %s (%d bytes) 已成功上传到远程服务器 %s，耗时 %v\n", 
		fileName, result.Size, finalFilePath, result.Duration)

	return result, nil
}

// UploadFiles 批量上传文件
func (fs *FileScanner) UploadFiles(filePaths []string) ([]*UploadResult, error) {
	var results []*UploadResult

	for _, filePath := range filePaths {
		result, err := fs.UploadFile(filePath)
		if err != nil {
			log.Printf("上传文件 %s 失败: %v\n", filePath, err)
		}
		results = append(results, result)
	}

	return results, nil
}

// EnsureRemoteDirExists 确保远程目录存在
func (fs *FileScanner) EnsureRemoteDirExists(dirPath string) error {
	// 确保使用Linux风格的路径分隔符
	dirPath = filepath.ToSlash(dirPath)
	return fs.sftpClient.MkdirAll(dirPath)
}

// GetRemoteFileExists 检查远程文件是否存在
func (fs *FileScanner) GetRemoteFileExists(filePath string) (bool, error) {
	// 确保使用Linux风格的路径分隔符
	filePath = filepath.ToSlash(filePath)
	_, err := fs.sftpClient.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// DeleteRemoteFile 删除远程文件
func (fs *FileScanner) DeleteRemoteFile(filePath string) error {
	// 确保使用Linux风格的路径分隔符
	filePath = filepath.ToSlash(filePath)
	return fs.sftpClient.Remove(filePath)
}

// RenameRemoteFile 重命名远程文件
func (fs *FileScanner) RenameRemoteFile(oldPath, newPath string) error {
	// 确保使用Linux风格的路径分隔符
	oldPath = filepath.ToSlash(oldPath)
	newPath = filepath.ToSlash(newPath)
	return fs.sftpClient.Rename(oldPath, newPath)
}