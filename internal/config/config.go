package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	// Server
	Port string `mapstructure:"PORT"`

	// Database
	DatabaseURL string `mapstructure:"DATABASE_URL"`

	// Auth
	JWTSecret         string `mapstructure:"JWT_SECRET"`
	InternalAPIToken  string `mapstructure:"INTERNAL_API_TOKEN"`
	FrontendURL       string `mapstructure:"FRONTEND_URL"`

	// GitHub OAuth (user login)
	GitHubClientID     string `mapstructure:"GITHUB_CLIENT_ID"`
	GitHubClientSecret string `mapstructure:"GITHUB_CLIENT_SECRET"`

	// Google OAuth / SSO (user login)
	GoogleClientID     string `mapstructure:"GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `mapstructure:"GOOGLE_CLIENT_SECRET"`

	// GitHub App (repo operations)
	GitHubAppID             string `mapstructure:"GITHUB_APP_ID"`
	GitHubAppPrivateKeyPath string `mapstructure:"GITHUB_APP_PRIVATE_KEY_PATH"`

	// LLM providers
	AnthropicAPIKey string `mapstructure:"ANTHROPIC_API_KEY"`
	OpenAIAPIKey    string `mapstructure:"OPENAI_API_KEY"`

	// Email
	ResendAPIKey string `mapstructure:"RESEND_API_KEY"`

	// AWS
	AWSS3Bucket string `mapstructure:"AWS_S3_BUCKET"`
	AWSRegion   string `mapstructure:"AWS_REGION"`

	// Nango integration platform
	NangoServerURL string `mapstructure:"NANGO_SERVER_URL"`
	NangoSecretKey string `mapstructure:"NANGO_SECRET_KEY"`
	NangoPublicKey string `mapstructure:"NANGO_PUBLIC_KEY"`

	// Intercom (native OAuth integration)
	IntercomClientID      string `mapstructure:"INTERCOM_CLIENT_ID"`
	IntercomClientSecret  string `mapstructure:"INTERCOM_CLIENT_SECRET"`
	IntercomWebhookSecret string `mapstructure:"INTERCOM_WEBHOOK_SECRET"`

	// Slack (native OAuth integration)
	SlackClientID     string `mapstructure:"SLACK_CLIENT_ID"`
	SlackClientSecret string `mapstructure:"SLACK_CLIENT_SECRET"`
	SlackSigningSecret string `mapstructure:"SLACK_SIGNING_SECRET"`

	// Linear (native OAuth integration)
	LinearClientID      string `mapstructure:"LINEAR_CLIENT_ID"`
	LinearClientSecret  string `mapstructure:"LINEAR_CLIENT_SECRET"`
	LinearWebhookSecret string `mapstructure:"LINEAR_WEBHOOK_SECRET"`

	// Jira (native OAuth integration — Atlassian OAuth 2.0 3LO)
	JiraClientID      string `mapstructure:"JIRA_CLIENT_ID"`
	JiraClientSecret  string `mapstructure:"JIRA_CLIENT_SECRET"`
	JiraWebhookSecret string `mapstructure:"JIRA_WEBHOOK_SECRET"`

	// Stripe billing
	StripeSecretKey     string `mapstructure:"STRIPE_SECRET_KEY"`
	StripeWebhookSecret string `mapstructure:"STRIPE_WEBHOOK_SECRET"`
	StripePriceStarter  string `mapstructure:"STRIPE_PRICE_STARTER"`
	StripePriceBuilder  string `mapstructure:"STRIPE_PRICE_BUILDER"`

	// Observability
	SentryDSN     string `mapstructure:"SENTRY_DSN"`
	AppEnv        string `mapstructure:"APP_ENV"`

	// Agent Sandbox / Codegen v2
	E2BAPIKey               string `mapstructure:"E2B_API_KEY"`
	SandboxProvider         string `mapstructure:"SANDBOX_PROVIDER"`
	SandboxTimeoutMinutes   int    `mapstructure:"SANDBOX_TIMEOUT_MINUTES"`
	SandboxMaxRetries       int    `mapstructure:"SANDBOX_MAX_RETRIES"`
	EncryptionKey           string `mapstructure:"ENCRYPTION_KEY"`
	SandboxMaxConcurrentOrg int    `mapstructure:"SANDBOX_MAX_CONCURRENT"`
	SandboxE2BTemplate      string `mapstructure:"SANDBOX_E2B_TEMPLATE"`
}

// Load reads configuration from environment variables (with optional .env file support
// via Viper's automatic environment binding). Returns a fully populated Config.
func Load() (*Config, error) {
	v := viper.New()

	// Set defaults for optional fields.
	v.SetDefault("PORT", "8080")
	v.SetDefault("FRONTEND_URL", "http://localhost:5173")
	v.SetDefault("AWS_REGION", "us-east-1")
	v.SetDefault("NANGO_SERVER_URL", "http://localhost:3003")
	v.SetDefault("APP_ENV", "development")
	v.SetDefault("SANDBOX_PROVIDER", "e2b")
	v.SetDefault("SANDBOX_TIMEOUT_MINUTES", 20)
	v.SetDefault("SANDBOX_MAX_RETRIES", 3)
	v.SetDefault("SANDBOX_MAX_CONCURRENT", 5)
	v.SetDefault("SANDBOX_E2B_TEMPLATE", "neuco-codegen")

	// Bind all environment variables automatically. Viper upper-cases the key
	// and checks the environment for a matching variable.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Explicitly bind each key so that mapstructure unmarshalling picks them up
	// even when the key hasn't been set with SetDefault.
	keys := []string{
		"PORT",
		"DATABASE_URL",
		"JWT_SECRET",
		"INTERNAL_API_TOKEN",
		"FRONTEND_URL",
		"GITHUB_CLIENT_ID",
		"GITHUB_CLIENT_SECRET",
		"GOOGLE_CLIENT_ID",
		"GOOGLE_CLIENT_SECRET",
		"GITHUB_APP_ID",
		"GITHUB_APP_PRIVATE_KEY_PATH",
		"ANTHROPIC_API_KEY",
		"OPENAI_API_KEY",
		"RESEND_API_KEY",
		"AWS_S3_BUCKET",
		"AWS_REGION",
		"NANGO_SERVER_URL",
		"NANGO_SECRET_KEY",
		"NANGO_PUBLIC_KEY",
		"INTERCOM_CLIENT_ID",
		"INTERCOM_CLIENT_SECRET",
		"INTERCOM_WEBHOOK_SECRET",
		"SLACK_CLIENT_ID",
		"SLACK_CLIENT_SECRET",
		"SLACK_SIGNING_SECRET",
		"LINEAR_CLIENT_ID",
		"LINEAR_CLIENT_SECRET",
		"LINEAR_WEBHOOK_SECRET",
		"JIRA_CLIENT_ID",
		"JIRA_CLIENT_SECRET",
		"JIRA_WEBHOOK_SECRET",
		"STRIPE_SECRET_KEY",
		"STRIPE_WEBHOOK_SECRET",
		"STRIPE_PRICE_STARTER",
		"STRIPE_PRICE_BUILDER",
		"SENTRY_DSN",
		"APP_ENV",
		"E2B_API_KEY",
		"SANDBOX_PROVIDER",
		"SANDBOX_TIMEOUT_MINUTES",
		"SANDBOX_MAX_RETRIES",
		"ENCRYPTION_KEY",
		"SANDBOX_MAX_CONCURRENT",
		"SANDBOX_E2B_TEMPLATE",
	}
	for _, k := range keys {
		if err := v.BindEnv(k); err != nil {
			return nil, fmt.Errorf("config: binding env var %s: %w", k, err)
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshalling config: %w", err)
	}

	return cfg, nil
}

// Validate panics if any required configuration field is absent. Call this
// during application startup before any services are initialised.
func (c *Config) Validate() {
	required := map[string]string{
		"DATABASE_URL": c.DatabaseURL,
		"JWT_SECRET":   c.JWTSecret,
	}

	var missing []string
	for name, val := range required {
		if strings.TrimSpace(val) == "" {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		panic(fmt.Sprintf("config: missing required environment variables: %s", strings.Join(missing, ", ")))
	}
}
