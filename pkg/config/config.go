package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration
type Config struct {
	FreeSwitchInstances []FSConfig    `yaml:"freeswitch_instances"`
	Storage             StorageConfig `yaml:"storage"`
	OpenTelemetry       OTelConfig    `yaml:"opentelemetry"`
	HTTP                HTTPConfig    `yaml:"http"`
	Logging             LoggingConfig `yaml:"logging"`
	Events              EventsConfig  `yaml:"events"`
}

// FSConfig represents FreeSWITCH connection configuration
type FSConfig struct {
	Name     string `yaml:"name"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Password string `yaml:"password"`
}

// StorageConfig represents storage backend configuration
type StorageConfig struct {
	Type  string       `yaml:"type"` // "memory" or "redis"
	Redis *RedisConfig `yaml:"redis,omitempty"`
}

// RedisConfig represents Redis connection configuration
type RedisConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

// OTelConfig represents OpenTelemetry configuration
type OTelConfig struct {
	Endpoint string            `yaml:"endpoint"`
	Headers  map[string]string `yaml:"headers,omitempty"`
	Insecure bool              `yaml:"insecure"`
}

// HTTPConfig represents HTTP server configuration
type HTTPConfig struct {
	Port int `yaml:"port"`
}

// LoggingConfig represents logging configuration
type LoggingConfig struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // json, text
}

// EventsConfig represents events processing configuration
type EventsConfig struct {
	RTCP bool `yaml:"rtcp"` // Enable RTCP events processing
	QoS  bool `yaml:"qos"`  // Enable QoS events processing
}

// Load loads configuration from file or environment variables
// Priority: config file → environment variables
func Load(configPath string) (*Config, error) {
	var cfg Config

	// Try to load from file first
	if configPath != "" {
		if err := loadFromFile(configPath, &cfg); err != nil {
			// If file doesn't exist, continue to env vars
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("failed to load config file: %w", err)
			}
		}
	}

	// Apply environment variable overrides
	if err := loadFromEnv(&cfg); err != nil {
		return nil, fmt.Errorf("failed to load config from environment: %w", err)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Set defaults
	cfg.setDefaults()

	return &cfg, nil
}

// loadFromFile loads configuration from YAML file
func loadFromFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	return nil
}

// loadFromEnv loads configuration from environment variables with FSAGENT_ prefix
func loadFromEnv(cfg *Config) error {
	// OpenTelemetry endpoint
	if endpoint := os.Getenv("FSAGENT_OTEL_ENDPOINT"); endpoint != "" {
		cfg.OpenTelemetry.Endpoint = endpoint
	}

	// OpenTelemetry insecure
	if insecure := os.Getenv("FSAGENT_OTEL_INSECURE"); insecure != "" {
		cfg.OpenTelemetry.Insecure = insecure == "true"
	}

	// HTTP port
	if port := os.Getenv("FSAGENT_HTTP_PORT"); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil {
			return fmt.Errorf("invalid FSAGENT_HTTP_PORT: %w", err)
		}
		cfg.HTTP.Port = p
	}

	// Logging level
	if level := os.Getenv("FSAGENT_LOG_LEVEL"); level != "" {
		cfg.Logging.Level = level
	}

	// Logging format
	if format := os.Getenv("FSAGENT_LOG_FORMAT"); format != "" {
		cfg.Logging.Format = format
	}

	// Events RTCP
	if rtcp := os.Getenv("FSAGENT_EVENTS_RTCP"); rtcp != "" {
		cfg.Events.RTCP = rtcp == "true"
	}

	// Events QoS
	if qos := os.Getenv("FSAGENT_EVENTS_QOS"); qos != "" {
		cfg.Events.QoS = qos == "true"
	}

	// Storage type
	if storageType := os.Getenv("FSAGENT_STORAGE_TYPE"); storageType != "" {
		cfg.Storage.Type = storageType
	}

	// Redis configuration
	if redisHost := os.Getenv("FSAGENT_REDIS_HOST"); redisHost != "" {
		if cfg.Storage.Redis == nil {
			cfg.Storage.Redis = &RedisConfig{}
		}
		cfg.Storage.Redis.Host = redisHost
	}

	if redisPort := os.Getenv("FSAGENT_REDIS_PORT"); redisPort != "" {
		if cfg.Storage.Redis == nil {
			cfg.Storage.Redis = &RedisConfig{}
		}
		p, err := strconv.Atoi(redisPort)
		if err != nil {
			return fmt.Errorf("invalid FSAGENT_REDIS_PORT: %w", err)
		}
		cfg.Storage.Redis.Port = p
	}

	if redisPassword := os.Getenv("FSAGENT_REDIS_PASSWORD"); redisPassword != "" {
		if cfg.Storage.Redis == nil {
			cfg.Storage.Redis = &RedisConfig{}
		}
		cfg.Storage.Redis.Password = redisPassword
	}

	if redisDB := os.Getenv("FSAGENT_REDIS_DB"); redisDB != "" {
		if cfg.Storage.Redis == nil {
			cfg.Storage.Redis = &RedisConfig{}
		}
		db, err := strconv.Atoi(redisDB)
		if err != nil {
			return fmt.Errorf("invalid FSAGENT_REDIS_DB: %w", err)
		}
		cfg.Storage.Redis.DB = db
	}

	// FreeSWITCH instances from environment
	// Format: FSAGENT_FREESWITCH_INSTANCES='[{"name":"fs1","host":"192.168.1.10","port":8021,"password":"ClueCon"}]'
	if fsInstances := os.Getenv("FSAGENT_FREESWITCH_INSTANCES"); fsInstances != "" {
		var instances []FSConfig
		if err := yaml.Unmarshal([]byte(fsInstances), &instances); err != nil {
			return fmt.Errorf("invalid FSAGENT_FREESWITCH_INSTANCES format (expected JSON array): %w", err)
		}
		if len(instances) > 0 {
			cfg.FreeSwitchInstances = instances
		}
	}

	return nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	// Validate FreeSWITCH instances
	if len(c.FreeSwitchInstances) == 0 {
		return fmt.Errorf("at least one FreeSWITCH instance must be configured")
	}

	for i, fs := range c.FreeSwitchInstances {
		if fs.Name == "" {
			return fmt.Errorf("FreeSWITCH instance %d: name is required", i)
		}
		if fs.Host == "" {
			return fmt.Errorf("FreeSWITCH instance %s: host is required", fs.Name)
		}
		if fs.Port <= 0 || fs.Port > 65535 {
			return fmt.Errorf("FreeSWITCH instance %s: invalid port %d", fs.Name, fs.Port)
		}
		if fs.Password == "" {
			return fmt.Errorf("FreeSWITCH instance %s: password is required", fs.Name)
		}
	}

	// Validate OpenTelemetry endpoint
	if c.OpenTelemetry.Endpoint == "" {
		return fmt.Errorf("OpenTelemetry endpoint is required")
	}

	// Validate storage type
	if c.Storage.Type != "" && c.Storage.Type != "memory" && c.Storage.Type != "redis" {
		return fmt.Errorf("storage type must be 'memory' or 'redis', got '%s'", c.Storage.Type)
	}

	// Validate Redis config if type is redis
	if c.Storage.Type == "redis" {
		if c.Storage.Redis == nil {
			return fmt.Errorf("Redis configuration is required when storage type is 'redis'")
		}
		if c.Storage.Redis.Host == "" {
			return fmt.Errorf("Redis host is required")
		}
		if c.Storage.Redis.Port <= 0 || c.Storage.Redis.Port > 65535 {
			return fmt.Errorf("invalid Redis port %d", c.Storage.Redis.Port)
		}
	}

	// Validate logging level
	if c.Logging.Level != "" {
		validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
		if !validLevels[c.Logging.Level] {
			return fmt.Errorf("invalid log level '%s', must be one of: debug, info, warn, error", c.Logging.Level)
		}
	}

	// Validate logging format
	if c.Logging.Format != "" {
		if c.Logging.Format != "json" && c.Logging.Format != "text" {
			return fmt.Errorf("invalid log format '%s', must be 'json' or 'text'", c.Logging.Format)
		}
	}

	return nil
}

// setDefaults sets default values for optional fields
func (c *Config) setDefaults() {
	// Default HTTP port
	if c.HTTP.Port == 0 {
		c.HTTP.Port = 8080
	}

	// Default logging level
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}

	// Default logging format
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}

	// Default storage type
	if c.Storage.Type == "" {
		c.Storage.Type = "memory"
	}

	// Default events - enable both by default
	// Note: These are not set if explicitly configured to false in YAML
}
