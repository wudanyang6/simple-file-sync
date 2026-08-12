/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/wudanyang6/simple-file-sync/client"
)

var (
	ClientUploadMode     string
	ClientLocalDir       string
	ClientRemoteDir      string
	ClientServerAddr     string
	ClientServerToken    string
	ClientIgnorePatterns []string
	ClientPathMappings   []string
	ClientConfigFile     string
	ClientTargetName     string
	ClientPropagateDeletes bool
	clientPropagateDeletesSet bool
)

// RemoteTargetConfig 表示远程目标配置
type RemoteTargetConfig struct {
	Name      string `toml:"name"`
	ServerAddr string `toml:"server_addr"`
	RemoteDir  string `toml:"remote_dir"`
	Token      string `toml:"token"`
	// PropagateDeletes 可选：nil=未配置，删除传播回退到全局 propagate_deletes
	PropagateDeletes *bool `toml:"propagate_deletes"`
}

// ClientConfig 表示客户端配置文件结构
type ClientConfig struct {
	Mode         string              `toml:"mode"`
	LocalDir     string              `toml:"local_dir"`
	RemoteTargets []RemoteTargetConfig `toml:"remote_targets"`
	ActiveTarget string              `toml:"active_target"`    // 当前激活的目标名称
	Ignore       []string            `toml:"ignore"`
	PathMappings []string            `toml:"path_mappings"`
	PropagateDeletes bool            `toml:"propagate_deletes"` // 是否将删除/重命名同步到远端，默认 false
}

// loadConfig 从TOML文件加载配置
func loadConfig(configFile string) (*ClientConfig, error) {
	// 默认配置文件
	if configFile == "" {
		configFile = "simple-file-sync.toml"
	}

	// 检查文件是否存在
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		// 如果明确指定了配置文件但不存在，返回错误
		if ClientConfigFile != "" {
			return nil, fmt.Errorf("配置文件 %s 不存在", configFile)
		}
		// 使用默认配置
		return &ClientConfig{}, nil
	}

	var config ClientConfig
	if _, err := toml.DecodeFile(configFile, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %v", err)
	}

	return &config, nil
}

// overrideConfigWithFlags 使用命令行参数覆盖配置文件设置
func overrideConfigWithFlags(config *ClientConfig) {
	// 只有当命令行参数有值时才覆盖配置文件中的值
	if ClientUploadMode != "" {
		config.Mode = ClientUploadMode
	}
	if ClientLocalDir != "" {
		config.LocalDir = ClientLocalDir
	}
	if ClientTargetName != "" {
		config.ActiveTarget = ClientTargetName
	}
	
	// 处理命令行参数，创建新的远程目标
	if ClientRemoteDir != "" || ClientServerAddr != "" || ClientServerToken != "" {
		// 必须同时提供所有三个参数
		if ClientRemoteDir == "" || ClientServerAddr == "" || ClientServerToken == "" {
			log.Printf("警告: 使用命令行创建远程目标需要同时提供 remote-dir, server-addr 和 server-token 参数")
			return
		}
		
		// 生成一个唯一的目标名称，使用时间戳
		targetName := fmt.Sprintf("cmdline_%d", time.Now().Unix())
		
		config.RemoteTargets = append(config.RemoteTargets, RemoteTargetConfig{
			Name:      targetName,
			RemoteDir:  ClientRemoteDir,
			ServerAddr: ClientServerAddr,
			Token:      ClientServerToken,
		})
		
		// 将新创建的目标设为活动目标
		config.ActiveTarget = targetName
	}
	
	if len(ClientIgnorePatterns) > 0 {
		config.Ignore = append(config.Ignore, ClientIgnorePatterns...)
	}
	if len(ClientPathMappings) > 0 {
		config.PathMappings = append(config.PathMappings, ClientPathMappings...)
	}
	if clientPropagateDeletesSet {
		config.PropagateDeletes = ClientPropagateDeletes
	}
}

// validateConfig 验证配置是否有效
func validateConfig(config *ClientConfig) error {
	if config.Mode == "" {
		config.Mode = "all" // 默认模式
	} else if config.Mode != "all" && config.Mode != "git" {
		return fmt.Errorf("不支持的模式: %s，支持的模式: all, git", config.Mode)
	}

	if config.LocalDir == "" {
		return fmt.Errorf("必须指定本地目录 (local_dir)")
	}
	
	// 验证是否至少有一个远程目标
	if len(config.RemoteTargets) == 0 {
		return fmt.Errorf("必须配置至少一个远程目标 (remote_targets)")
	}
	
	// 验证每个远程目标的配置
	for _, target := range config.RemoteTargets {
		if target.ServerAddr == "" {
			return fmt.Errorf("远程目标 '%s' 必须指定服务器地址 (server_addr)", target.Name)
		}
		if target.RemoteDir == "" {
			return fmt.Errorf("远程目标 '%s' 必须指定远程目录 (remote_dir)", target.Name)
		}
	}
	
	// 如果没有设置活动目标，使用第一个目标
	if config.ActiveTarget == "" {
		config.ActiveTarget = config.RemoteTargets[0].Name
		log.Printf("未指定活动目标，使用 '%s'", config.ActiveTarget)
	}
	
	// 确认活动目标存在
	targetExists := false
	for _, target := range config.RemoteTargets {
		if target.Name == config.ActiveTarget {
			targetExists = true
			break
		}
	}
	
	if !targetExists {
		return fmt.Errorf("指定的活动目标 '%s' 不存在", config.ActiveTarget)
	}

	return nil
}

// clientCmd represents the client command
var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "client for simple file sync",
	Long: `A client for simple file sync. For example:
 	simple-file-sync client --local-dir=/Users/wudanyang/self/simple-file-sync --mode=all --remote-dir=/Users/wudanyang/self/testforsimple --server-addr=http://127.0.0.1:8120/receiver --server-token=something
 	
 	You can specify files to ignore using regular expressions:
 	--ignore="\.tmp$,node_modules,build"
 	
 	You can specify path mappings using regular expressions:
 	--mapping="/local/path(/.*):/remote/path$1,/local/src(/.+):/remote/build$1"
 	
 	For mapping, the source is a regular expression with capture groups, and target can use $1, $2, etc. to reference captured groups.
 	For example, "/local/path(/.*)" will capture everything after "/local/path" in $1, which can then be referenced in the target path.
 	
 	You can specify which remote target to use:
 	--target=production
 	
 	You can also specify a configuration file in TOML format:
 	-c config.toml or --config=config.toml
 	
 	Example configuration file (simple-file-sync.toml):
 	
 	mode = "all"
 	local_dir = "/Users/wudanyang/self/simple-file-sync"
 	active_target = "dev"
 	
 	# 远程目标配置
 	[[remote_targets]]
 	name = "dev"
 	server_addr = "http://127.0.0.1:8120/receiver"
 	remote_dir = "/Users/wudanyang/self/testforsimple"
 	token = "something"
 	
 	[[remote_targets]]
 	name = "production"
 	server_addr = "http://example.com:8120/receiver"
 	remote_dir = "/var/www/production"
 	token = "production-token"
 	
 	ignore = ["\\.tmp$", "node_modules", "build"]
 	path_mappings = ["/local/path(/.*):remote/path$1"]
 	`,
	Run: func(cmd *cobra.Command, args []string) {
		// 加载配置文件
		config, err := loadConfig(ClientConfigFile)
		if err != nil {
			log.Fatalf("加载配置失败: %v", err)
		}

		// 仅当用户在命令行显式提供 --propagate-deletes 时才覆盖配置文件值
		clientPropagateDeletesSet = cmd.Flags().Changed("propagate-deletes")

		// 使用命令行参数覆盖配置文件
		overrideConfigWithFlags(config)

		// 验证配置
		if err := validateConfig(config); err != nil {
			log.Fatalf("配置验证失败: %v", err)
		}

		c, err := buildClient(config)
		if err != nil {
			log.Fatalf("初始化客户端失败: %v", err)
		}
		c.Start()
	},
}

// buildClient wires a *client.Client from a validated ClientConfig.
// Exposed for tests; does not call Start.
func buildClient(config *ClientConfig) (*client.Client, error) {
	c := client.NewClient(config.Mode, config.LocalDir)
	c.PropagateDeletes = config.PropagateDeletes

	for _, target := range config.RemoteTargets {
		c.AddRemoteTargetWithDeletes(target.Name, target.ServerAddr, target.RemoteDir, target.Token, target.PropagateDeletes)
	}

	if err := c.SetActiveTarget(config.ActiveTarget); err != nil {
		return nil, fmt.Errorf("设置活动目标失败: %w", err)
	}

	for _, pattern := range config.Ignore {
		c.AddIgnorePattern(pattern)
	}

	for _, mapping := range config.PathMappings {
		parts := strings.Split(mapping, ":")
		if len(parts) == 2 {
			c.AddPathMapping(parts[0], parts[1])
		}
	}

	var activeTarget *RemoteTargetConfig
	for i := range config.RemoteTargets {
		if config.RemoteTargets[i].Name == config.ActiveTarget {
			activeTarget = &config.RemoteTargets[i]
			break
		}
	}
	if activeTarget == nil {
		return nil, fmt.Errorf("找不到活动目标: %s", config.ActiveTarget)
	}

	if config.LocalDir != "" && activeTarget.RemoteDir != "" {
		source := "^" + regexp.QuoteMeta(config.LocalDir) + "(/.*)?$"
		target := activeTarget.RemoteDir + "$1"
		c.AddPathMapping(source, target)
	}

	return c, nil
}

func init() {
	rootCmd.AddCommand(clientCmd)
	clientCmd.Flags().StringVar(&ClientUploadMode, "mode", "", "upload mode, sync or async")
	clientCmd.Flags().StringVar(&ClientLocalDir, "local-dir", "", "local directory")
	clientCmd.Flags().StringVar(&ClientRemoteDir, "remote-dir", "", "remote directory")
	clientCmd.Flags().StringVar(&ClientServerAddr, "server-addr", "", "server address")
	clientCmd.Flags().StringVar(&ClientServerToken, "server-token", "", "server token")
	clientCmd.Flags().StringVar(&ClientTargetName, "target", "", "name of the remote target to use")

	// 添加ignore和mapping的flags
	clientCmd.Flags().StringSliceVar(&ClientIgnorePatterns, "ignore", []string{}, "regex patterns to ignore, comma separated, e.g. \\.tmp$,node_modules")
	clientCmd.Flags().StringSliceVar(&ClientPathMappings, "mapping", []string{}, "path mappings using regex, format: source-regex:target-template, comma separated, e.g. /local/path(/.*):remote/path$1")

	// 添加配置文件选项
	clientCmd.Flags().StringVarP(&ClientConfigFile, "config", "c", "", "configuration file path (default \"simple-file-sync.toml\")")

	// 删除/重命名是否传播到远端（默认关闭，需显式开启）
	clientCmd.Flags().BoolVar(&ClientPropagateDeletes, "propagate-deletes", false, "propagate local file deletions/renames to the remote (default false)")
}
