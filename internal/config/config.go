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
	Display DisplayConfig `yaml:"display"`
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

// DisplayConfig controls the optional front-panel status screen for a
// Raspberry Pi fitted with a Pimoroni Display HAT Mini. The feature
// auto-detects its hardware and no-ops cleanly when absent, so these
// settings only matter on a HAT-equipped Pi.
type DisplayConfig struct {
	// Enabled is a tri-state: nil/absent = auto-detect the HAT; true =
	// force the attempt (useful to surface init errors); false = never
	// touch the display hardware.
	Enabled         *bool    `yaml:"enabled"`
	RefreshInterval Duration `yaml:"refreshInterval"` // periodic redraw cadence
	Brightness      int      `yaml:"brightness"`      // backlight, 0-100
	Rotation        int      `yaml:"rotation"`        // 0 or 180 — HAT mounting orientation
	// AllowShutdown gates the button-Y long-press safe-shutdown. Off by
	// default: it runs `sudo systemctl poweroff`, which needs a NOPASSWD
	// sudoers rule for the service user.
	AllowShutdown bool `yaml:"allowShutdown"`
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
			Width:    500,
			Height:   300,
		},
		ADB: ADBConfig{
			ServerHost: "127.0.0.1",
			ServerPort: 5037,
			Timeout:    Duration(30 * time.Second),
			KeyPath:    "auto",
		},
		Logging: LoggingConfig{Level: "info"},
		Display: DisplayConfig{
			RefreshInterval: Duration(5 * time.Second),
			Brightness:      80,
			Rotation:        180,
			AllowShutdown:   false,
		},
	}
}

// Load reads config from path, or the platform default if path == "".
// Missing file → defaults (not an error). When path resolves to the
// platform default and no file exists there yet, Load also seeds a
// starter config so users have a discoverable place to edit (the
// auth.token in particular is impossible to find otherwise). Explicit
// --config paths are left alone — those are the user's call.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
		if created, _ := EnsureExists(path); created {
			// One-shot stderr notice so the user knows the file landed
			// there. Goes to bare stderr (not slog) because Load runs
			// before any logger is constructed, and the message is
			// useful regardless of configured log level.
			fmt.Fprintf(os.Stderr, "kam-transfer: wrote starter config to %s\n", path)
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

// EnsureExists writes a starter config at path if no file exists
// there. Parent directories are created (0o700). Existing files are
// left untouched — this is not a "reset to defaults" tool. Returns
// (true, nil) when a file was created. Errors are returned but
// callers typically treat them as best-effort: a daemon that can't
// write its config file can still run with built-in defaults.
func EnsureExists(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(starterConfigYAML), 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// starterConfigYAML is the comment-rich seed dropped on first run.
// Values mirror Default() so leaving any line alone is a no-op.
const starterConfigYAML = `# kam-transfer config — auto-generated on first run.
# Edit and restart the daemon for changes to take effect. Every value
# below is shown at its built-in default, so deleting any line is
# equivalent to keeping it.

server:
  port: 8765
  bind: 127.0.0.1
  # Origins allowed for browser CORS. Use "*" to accept any origin
  # (the request's Origin is echoed back; the literal "*" is forbidden
  # alongside Allow-Credentials: true so we mirror it instead).
  corsOrigins:
    - http://localhost:5173
    - http://127.0.0.1:5173
  readTimeout: 30s
  writeTimeout: 5m

# auth.token gates /api/* and the WebSocket. Empty = no auth (safe
# only on a single-user, single-machine setup). The admin UI at /ui
# is intentionally never gated regardless of this setting. Generate a
# token with e.g. ` + "`openssl rand -base64 24`" + `.
auth:
  token: ""

map:
  provider: esri-world-imagery   # "esri-world-imagery" | "solid"
  width: 500
  height: 300
  tileSize: 256

adb:
  serverHost: 127.0.0.1
  serverPort: 5037
  timeout: 30s
  keyPath: auto

logging:
  level: info                    # debug | info | warn | error
  file: ""                       # empty = stderr only

# Optional front-panel status screen for a Raspberry Pi fitted with a
# Pimoroni Display HAT Mini + PiSugar 3. The feature auto-detects its
# hardware and is a silent no-op when absent — these settings only
# matter on a HAT-equipped Pi.
display:
  # enabled: leave unset to auto-detect the HAT. Set false to disable
  # entirely, or true to force the attempt (surfaces init errors).
  # enabled: true
  refreshInterval: 5s            # how often the screen redraws
  brightness: 80                 # backlight, 0-100
  rotation: 180                  # 0 or 180, to match how the HAT is mounted
  # allowShutdown: holding button Y for 3s runs "sudo systemctl
  # poweroff". Off by default; needs a NOPASSWD sudoers rule.
  allowShutdown: false
`

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
