package config

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL       string
	ExBaseURL         string
	Cookies           map[string]string
	ProxyURL          string
	ThumbDir          string
	RateInterval      float64
	ThumbRateInterval float64
	BanCooldown       float64
	Headers           http.Header
}

func Load() (*Config, error) {
	// Try loading .env from various relative paths to project root
	_ = godotenv.Load("../../.env") // from subpackage test
	_ = godotenv.Load("../.env")    // from scraper-go root
	_ = godotenv.Load(".env")

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is not set")
	}

	baseURL := getEnv("EX_BASE_URL", "https://exhentai.org")

	cookies := parseCookies(os.Getenv("EX_COOKIES"))

	cfg := &Config{
		DatabaseURL:       dbURL,
		ExBaseURL:         baseURL,
		Cookies:           cookies,
		ProxyURL:          os.Getenv("PROXY_URL"),
		ThumbDir:          getEnv("THUMB_DIR", "/data/thumbs"),
		RateInterval:      getEnvFloat("RATE_INTERVAL", 5.0),
		ThumbRateInterval: getEnvFloat("THUMB_RATE_INTERVAL", 0.5),
		BanCooldown:       getEnvFloat("BAN_COOLDOWN", 60.0),
		Headers: http.Header{
			"User-Agent": {
				"Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
					"AppleWebKit/537.36 (KHTML, like Gecko) " +
					"Chrome/124.0.0.0 Safari/537.36",
			},
			"Accept-Language": {"en-US,en;q=0.9"},
			"Referer":         {baseURL},
		},
	}
	return cfg, nil
}

func parseCookies(raw string) map[string]string {
	cookies := make(map[string]string)
	for _, pair := range strings.Split(raw, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		cookies[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return cookies
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}
