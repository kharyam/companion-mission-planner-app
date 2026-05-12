package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Map     MapConfig     `yaml:"map"`
	ADB     ADBConfig     `yaml:"adb"`
	Logging LoggingConfig `yaml:"logging"`
	Auth    AuthConfig    `yaml:"auth"`
}

type ServerConfig struct {
	Port         int      `yaml:"port"`
	Bind         string   `yaml:"bind"`
	CORSOrigins  []string `yaml:"corsOrigins"`
	ReadTimeout  Duration `yaml:"readTimeout"`
	WriteTimeout Duration `yaml:"writeTimeout"`
}

type MapConfig struct {
	Provider string `yaml:"provider"` // "esri-world-imagery" | "solid"
	APIKey   string `yaml:"apiKey"`
	TileSize int    `yaml:"tileSize"`
	Width    int    `yaml:"width"`
	Height   int    `yaml:"height"`
}

type ADBConfig struct {
	ServerHost string   `yaml:"serverHost"`
	ServerPort int      `yaml:"serverPort"`
	Timeout    Duration `yaml:"timeout"`
	KeyPath    string   `yaml:"keyPath"`
}

type LoggingConfig struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

type AuthConfig struct {
	Token string `yaml:"token"` // empty disables token auth
}

// Duration is a yaml-friendly time.Duration.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8765,
			Bind: "127.0.0.1",
			CORSOrigins: []string{
				"http://localhost:5173",
				"http://127.0.0.1:5173",
			},
			ReadTimeout:  Duration(30 * time.Second),
			WriteTimeout: Duration(5 * time.Minute),
		},
		Map: MapConfig{
			Provider: "esri-world-imagery",
			TileSize: 256,
			Width:    1024,
			Height:   768,
		},
		ADB: ADBConfig{
			ServerHost: "127.0.0.1",
			ServerPort: 5037,
			Timeout:    Duration(30 * time.Second),
			KeyPath:    "auto",
		},
		Logging: LoggingConfig{Level: "info"},
	}
}

// Load reads config from path, or the platform default if path == "".
// Missing file → defaults (not an error).
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// DefaultPath returns the platform-appropriate config path.
func DefaultPath() (string, error) {
	switch runtime.GOOS {
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			return "", errors.New("APPDATA not set")
		}
		return filepath.Join(appdata, "kam-transfer", "config.yaml"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "kam-transfer", "config.yaml"), nil
	default:
		cfgHome := os.Getenv("XDG_CONFIG_HOME")
		if cfgHome == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			cfgHome = filepath.Join(home, ".config")
		}
		return filepath.Join(cfgHome, "kam-transfer", "config.yaml"), nil
	}
}
