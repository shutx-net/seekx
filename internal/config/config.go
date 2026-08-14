package config

// package config は環境変数と .env からプロセス全体の設定を解決する。
//
// 制約（file_layout）:
//   - 他の internal パッケージを import しない（このパッケージは依存グラフの葉である）
//   - トークンの値をログ・エラーメッセージに出さない（P-TOKEN-LEAK）

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	defaultAPIBaseURL    = "https://api.x.com/2"
	defaultMaxResultsCap = 100
	defaultDisplayTZ     = "Asia/Tokyo"
	defaultEnvFile       = ".env"

	// defaultBindAddr はループバックに絞る。安全な側を既定にするため。
	// ホストで直接動かす場合はこれで届き、LAN からは TCP レベルで到達できない。
	//
	// コンテナ内のループバックはホストから到達できないので、devcontainer では
	// SEEKX_BIND=0.0.0.0 が要る（.devcontainer/go/devcontainer.json の containerEnv
	// で指定している）。0.0.0.0 を既定としていた D-08 / P-BIND-0000 はここで反転し、
	// 「広く開けるほうを明示させる」向きに変えている。
	defaultBindAddr = "127.0.0.1"
)

// ErrNoBearerToken は BEARER_TOKEN が解決できなかったことを表す。
//
// フィクスチャモード以外では検索に必須だが、起動後に UI から設定できるため、
// 起動を止めるかどうかは呼び出し側（cmd/seekx）が errors.Is で判断する。
var ErrNoBearerToken = errors.New("BEARER_TOKEN が設定されていません")

// DefaultAllowedHosts は Host ヘッダのホスト名として既定で許可する値を返す。
//
// サーバは認証を持たないため、ブラウザ経由の到達をホスト名で絞る。
// 攻撃者のドメインが 127.0.0.1 へ解決するよう仕込まれても（DNS rebinding）、
// ブラウザが送る Host ヘッダは攻撃者のドメインのままなので落ちる。
//
// この検証はバインド先に関わらず必要。DNS rebinding が狙うのは 127.0.0.1 なので、
// ループバックに絞っても防げない（バインドで防げるのは LAN からの直接アクセスの側）。
//
// 呼び出しごとに新しいスライスを返す（パッケージ変数だと呼び出し側から書き換えられる）。
func DefaultAllowedHosts() []string {
	return []string{"localhost", "127.0.0.1", "::1"}
}

// NormalizeHostname は Host ヘッダや設定値からホスト名部分だけを取り出し、
// 比較可能な形（ポートなし・角括弧なし・末尾ドットなし・小文字）に正規化する。
//
// 許可リスト側と検証側で正規化がずれると許可リストを迂回できてしまうため、
// 両者がこの 1 つの関数を共有する（cmd/seekx の Host 検証もこれを使う）。
func NormalizeHostname(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if hostOnly, _, err := net.SplitHostPort(h); err == nil {
		h = hostOnly
	} else {
		// ポートを伴わない IPv6 リテラル（"[::1]" や素の "::1"）はここに来る。
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	}
	h = strings.TrimSuffix(h, ".") // FQDN 形式の "localhost."
	return strings.ToLower(h)
}

// parseAllowedHosts は SEEKX_ALLOWED_HOSTS（カンマ区切り）を既定値に追加する。
// 既定値を置き換えるのではなく追加するのは、localhost を失う設定ミスを防ぐため。
// ポート番号が書かれていても無視する（比較はホスト名だけで行う）。
func parseAllowedHosts(raw string) []string {
	hosts := DefaultAllowedHosts()
	seen := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		seen[h] = true
	}
	for _, part := range strings.Split(raw, ",") {
		h := NormalizeHostname(part)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		hosts = append(hosts, h)
	}
	return hosts
}

// Config はプロセス全体の設定。環境変数と .env から解決する（D-09）。
type Config struct {
	Port          string // PORT、既定 DefaultPort
	BindAddr      string // SEEKX_BIND、既定 defaultBindAddr
	BearerToken   string // BEARER_TOKEN（フィクスチャモード以外では必須）
	APIBaseURL    string // SEEKX_API_BASE、既定 https://api.x.com/2
	FixtureDir    string // SEEKX_FIXTURE_DIR、非空ならフィクスチャモード
	FixtureFile   string // SEEKX_FIXTURE_FILE、個別上書き
	RawDumpDir    string // SEEKX_RAW_DUMP_DIR、非空なら生レスポンスを保存（フィクスチャ作成用）
	AllowOverCap  bool   // SEEKX_ALLOW_OVER_CAP=1 で cap 95% 超でも実行許可
	MaxResultsCap int    // SEEKX_MAX_RESULTS_CAP、既定 100
	DisplayTZ     string // SEEKX_TZ、既定 Asia/Tokyo
	EnvFile       string // SEEKX_ENV_FILE、既定 .env
	LogLevel      string // SEEKX_LOG_LEVEL、既定 info

	// AllowedHosts は Host ヘッダとして受け付けるホスト名。
	// DefaultAllowedHosts に SEEKX_ALLOWED_HOSTS を追加したもの。
	AllowedHosts []string
}

// FixtureMode はフィクスチャモード（課金ゼロ）かどうかを返す（D-22）。
func (c Config) FixtureMode() bool { return c.FixtureDir != "" }

// Location は表示・入力に使うタイムゾーンを返す（D-18）。解決できない場合は UTC。
//
// 解決できないまま UTC で動くと全時刻表示が黙ってずれるため、起動時に一度だけ
// LocationError で検知して警告する（ここで警告するとページ描画のたびに出てしまう）。
func (c Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.DisplayTZ)
	if err != nil {
		return time.UTC
	}
	return loc
}

// LocationError はタイムゾーンを解決できなかった場合にその理由を返す。
// 解決できる場合は nil。起動時の警告に使う。
func (c Config) LocationError() error {
	_, err := time.LoadLocation(c.DisplayTZ)
	return err
}

// ListenAddr は ListenAndServe に渡すアドレスを返す。
// net.JoinHostPort を通すのは、IPv6 リテラル（"::1"）を角括弧で囲む必要があるため。
func (c Config) ListenAddr() string {
	return net.JoinHostPort(c.BindAddr, c.Port)
}

// BindsLoopback はバインド先がループバックかどうかを返す。
// コンテナ内でループバックに絞るとホストのブラウザから到達できず、原因が分かりにくい。
// 起動時にその案内を出すかどうかの判定に使う。
func (c Config) BindsLoopback() bool {
	if strings.EqualFold(strings.TrimSpace(c.BindAddr), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(c.BindAddr))
	return ip != nil && ip.IsLoopback()
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
		Port:          resolvePort(func(k string) string { return lookupEnv(dot, k) }),
		BindAddr:      strings.TrimSpace(lookupEnv(dot, "SEEKX_BIND")),
		BearerToken:   lookupEnv(dot, "BEARER_TOKEN"),
		APIBaseURL:    lookupEnv(dot, "SEEKX_API_BASE"),
		FixtureDir:    lookupEnv(dot, "SEEKX_FIXTURE_DIR"),
		FixtureFile:   lookupEnv(dot, "SEEKX_FIXTURE_FILE"),
		RawDumpDir:    lookupEnv(dot, "SEEKX_RAW_DUMP_DIR"),
		AllowOverCap:  lookupEnv(dot, "SEEKX_ALLOW_OVER_CAP") == "1",
		MaxResultsCap: defaultMaxResultsCap,
		DisplayTZ:     lookupEnv(dot, "SEEKX_TZ"),
		EnvFile:       envFile,
		LogLevel:      strings.ToLower(lookupEnv(dot, "SEEKX_LOG_LEVEL")),
		AllowedHosts:  parseAllowedHosts(lookupEnv(dot, "SEEKX_ALLOWED_HOSTS")),
	}
	if cfg.BindAddr == "" {
		cfg.BindAddr = defaultBindAddr
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
	if v := lookupEnv(dot, "SEEKX_MAX_RESULTS_CAP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 10 {
			return Config{}, fmt.Errorf("SEEKX_MAX_RESULTS_CAP は 10 以上の整数で指定してください")
		}
		cfg.MaxResultsCap = n
	}

	// フィクスチャモードでは API を叩かないためトークンは不要（D-22）。
	// トークンは UI からも設定できるため、これを致命的な起動失敗として扱うかどうかは
	// 呼び出し側が決める。ここでは事実だけを ErrNoBearerToken で伝える。
	if cfg.BearerToken == "" && !cfg.FixtureMode() {
		return cfg, fmt.Errorf("%w（起動後に設定画面から入力してください。起動のたびの入力を避けたい場合に限り %s に BEARER_TOKEN=... を書けますが、その場合トークンはディスク上に残ります。課金ゼロで UI を確認したい場合は SEEKX_FIXTURE_DIR=testdata を指定してください）", ErrNoBearerToken, envFile)
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
