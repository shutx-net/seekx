package config

// package config は環境変数と .env からプロセス全体の設定を解決する。
//
// 制約（file_layout）:
//   - 他の internal パッケージを import しない（このパッケージは依存グラフの葉である）
//   - トークンの値をログ・エラーメッセージに出さない（P-TOKEN-LEAK）

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultPort はローカル UI のポート番号の単一情報源（D-06 / D-07）。
// この値は .devcontainer/go/devcontainer.json の forwardPorts と一致していなければならない。
// ポート番号のリテラルはコード中この 1 箇所にのみ存在させる（P-PORT-SSOT）。
const DefaultPort = "5050"

const (
	defaultAPIBaseURL       = "https://api.x.com/2"
	defaultSessionBudgetUSD = 5.00
	defaultMaxResultsCap    = 100
	defaultDisplayTZ        = "Asia/Tokyo"
	defaultEnvFile          = ".env"
)

// Config はプロセス全体の設定。環境変数と .env から解決する（D-09）。
type Config struct {
	Port             string  // PORT、既定 DefaultPort
	BearerToken      string  // BEARER_TOKEN（フィクスチャモード以外では必須）
	APIBaseURL       string  // SEEKX_API_BASE、既定 https://api.x.com/2
	FixtureDir       string  // SEEKX_FIXTURE_DIR、非空ならフィクスチャモード
	FixtureFile      string  // SEEKX_FIXTURE_FILE、個別上書き
	RawDumpDir       string  // SEEKX_RAW_DUMP_DIR、非空なら生レスポンスを保存（フィクスチャ作成用）
	SessionBudgetUSD float64 // SEEKX_SESSION_BUDGET_USD、既定 5.00
	AllowOverCap     bool    // SEEKX_ALLOW_OVER_CAP=1 で cap 95% 超でも実行許可
	MaxResultsCap    int     // SEEKX_MAX_RESULTS_CAP、既定 100
	DisplayTZ        string  // SEEKX_TZ、既定 Asia/Tokyo
	EnvFile          string  // SEEKX_ENV_FILE、既定 .env
	LogLevel         string  // SEEKX_LOG_LEVEL、既定 info
}

// FixtureMode はフィクスチャモード（課金ゼロ）かどうかを返す（D-22）。
func (c Config) FixtureMode() bool { return c.FixtureDir != "" }

// Location は表示・入力に使うタイムゾーンを返す（D-18）。解決できない場合は UTC。
func (c Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.DisplayTZ)
	if err != nil {
		return time.UTC
	}
	return loc
}

// resolvePort は PORT 環境変数を優先し、未設定なら DefaultPort を返す（D-07）。
func resolvePort(get func(string) string) string {
	if v := get("PORT"); v != "" {
		return v
	}
	return DefaultPort
}

// loadDotEnv は .env を読み、キーと値のマップを返す。
// ファイルが存在しない場合は (nil, nil) を返しエラーにしない。
// 値は絶対にログ・エラーメッセージに出さない（P-TOKEN-LEAK）。
func loadDotEnv(path string) (map[string]string, error) {
	m, _, err := parseDotEnvFile(path)
	return m, err
}

// parseDotEnvFile は loadDotEnv の実体。スキップした行の行番号も返す（内容は返さない）。
func parseDotEnvFile(path string) (map[string]string, []int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf(".env の読み込みに失敗しました（パス: %s）: %w", path, err)
	}
	defer f.Close()

	m := make(map[string]string)
	var skipped []int

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if lineNo == 1 {
			line = strings.TrimPrefix(line, "\ufeff") // UTF-8 BOM
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, found := strings.Cut(line, "=")
		if !found {
			skipped = append(skipped, lineNo)
			continue
		}
		key = strings.TrimSpace(key)
		key = strings.TrimPrefix(key, "export ")
		key = strings.TrimSpace(key)
		if key == "" {
			skipped = append(skipped, lineNo)
			continue
		}
		val = strings.TrimSpace(val)
		if utf8.RuneCountInString(val) >= 2 {
			if q := val[0]; (q == '\'' || q == '"') && val[len(val)-1] == q {
				val = val[1 : len(val)-1]
			}
		}
		m[key] = val // 同一キーは後勝ち
	}
	if err := sc.Err(); err != nil {
		return nil, skipped, fmt.Errorf(".env の走査に失敗しました（パス: %s）: %w", path, err)
	}
	return m, skipped, nil
}

// lookupEnv は os.Getenv を優先し、空なら .env の値を使う（D-09）。
func lookupEnv(dot map[string]string, key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dot[key]
}

// MaskToken はトークンをログに出せる形へマスクする。値そのものは復元できない。
func MaskToken(s string) string {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return "(未設定)"
	}
	r := []rune(s)
	head := 4
	if n < head {
		head = n
	}
	return fmt.Sprintf("%s... (%d chars)", string(r[:head]), n)
}

// LoadConfig は環境変数と .env から Config を組み立てる。
// エラーメッセージにはキー名のみを含め、値は含めない。
func LoadConfig() (Config, error) {
	envFile := os.Getenv("SEEKX_ENV_FILE")
	if envFile == "" {
		envFile = defaultEnvFile
	}

	dot, skipped, err := parseDotEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	for _, ln := range skipped {
		slog.Warn(".env の行を読み飛ばしました（= がありません）", "file", envFile, "line", ln)
	}
	if dot != nil {
		slog.Info(".env を読み込みました", "file", envFile, "keys", len(dot))
	}

	cfg := Config{
		Port:             resolvePort(func(k string) string { return lookupEnv(dot, k) }),
		BearerToken:      lookupEnv(dot, "BEARER_TOKEN"),
		APIBaseURL:       lookupEnv(dot, "SEEKX_API_BASE"),
		FixtureDir:       lookupEnv(dot, "SEEKX_FIXTURE_DIR"),
		FixtureFile:      lookupEnv(dot, "SEEKX_FIXTURE_FILE"),
		RawDumpDir:       lookupEnv(dot, "SEEKX_RAW_DUMP_DIR"),
		SessionBudgetUSD: defaultSessionBudgetUSD,
		AllowOverCap:     lookupEnv(dot, "SEEKX_ALLOW_OVER_CAP") == "1",
		MaxResultsCap:    defaultMaxResultsCap,
		DisplayTZ:        lookupEnv(dot, "SEEKX_TZ"),
		EnvFile:          envFile,
		LogLevel:         strings.ToLower(lookupEnv(dot, "SEEKX_LOG_LEVEL")),
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = defaultAPIBaseURL
	}
	cfg.APIBaseURL = strings.TrimSuffix(cfg.APIBaseURL, "/")
	if cfg.DisplayTZ == "" {
		cfg.DisplayTZ = defaultDisplayTZ
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if v := lookupEnv(dot, "SEEKX_SESSION_BUDGET_USD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			return Config{}, fmt.Errorf("SEEKX_SESSION_BUDGET_USD は 0 以上の数値で指定してください")
		}
		cfg.SessionBudgetUSD = f
	}
	if v := lookupEnv(dot, "SEEKX_MAX_RESULTS_CAP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 10 {
			return Config{}, fmt.Errorf("SEEKX_MAX_RESULTS_CAP は 10 以上の整数で指定してください")
		}
		cfg.MaxResultsCap = n
	}

	// フィクスチャモードでは API を叩かないためトークンは不要（D-22）。
	if cfg.BearerToken == "" && !cfg.FixtureMode() {
		return cfg, fmt.Errorf("BEARER_TOKEN が見つかりません。ワークスペース直下の %s に BEARER_TOKEN=... を設定してください（課金ゼロで UI を確認したい場合は SEEKX_FIXTURE_DIR=testdata を指定してください）", envFile)
	}
	return cfg, nil
}

// SlogLevel は SEEKX_LOG_LEVEL を slog.Level に変換する。
func SlogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
