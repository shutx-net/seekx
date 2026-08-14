package main

// token.go は実行中の Bearer Token の保持と差し替えを担う。
//
// なぜ internal/config ではなくここか:
//   internal/config は起動時に環境変数と .env を解決する「読み取り」の担当で、
//   解決後は書き換わらない前提でプロセス全体から参照される。UI から差し替わる値を
//   そこに混ぜるとその前提が崩れる。実行時に可変な状態はプロセス側で持つ。
//
// 永続化しない理由:
//   ディスクに書くと、書き込み先のパーミッション・.gitignore の抜け・
//   .env を書き戻す際のロスト（parseDotEnvFile はコメントと引用符を捨てる）といった
//   別種のリスクが増える。プロセスが終われば UI 設定は消える、という単純さを選ぶ。
//   起動のたびに入力したくない場合は従来どおり .env に書く。
//
// 制約（file_layout）:
//   - トークンの値をログ・エラーメッセージ・HTML・URL に出さない（P-TOKEN-LEAK）
//   - ディスクに書かない
//   - HTTP に触らない（ハンドラは main.go の責務）
//   - フィクスチャモードの判定を持たない（config.FixtureMode が単一情報源）

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// maxTokenLen は受け付けるトークンの最大長。
// 実際の Bearer Token は 100 文字程度で、これは誤入力を弾くための上限。
const maxTokenLen = 1024

// tokenSource はいま有効なトークンの出所。
type tokenSource int

const (
	tokenSourceNone tokenSource = iota
	tokenSourceEnv
	tokenSourceUI
)

// String は画面とログに出す表示名。値そのものは含まない。
func (s tokenSource) String() string {
	switch s {
	case tokenSourceEnv:
		return ".env / 環境変数"
	case tokenSourceUI:
		return "設定画面（メモリのみ）"
	default:
		return "未設定"
	}
}

// tokenStore は .env 由来のトークンと設定画面から入力されたトークンを保持する。
// 画面からの設定が優先で、クリアすると .env 由来に戻る。
//
// 2 つを別々に持つのは、クリアの意味を「.env の値に戻す」にするため。
// 1 つのフィールドに上書きすると、クリアしたときに .env の値まで失われる。
type tokenStore struct {
	mu  sync.RWMutex
	env string // 起動時に config が解決した値（不変）
	ui  string // 設定画面から入力された値（空なら未設定）
}

func newTokenStore(envToken string) *tokenStore {
	return &tokenStore{env: envToken}
}

// Effective はいま使うべきトークンとその出所を返す。
func (s *tokenStore) Effective() (string, tokenSource) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ui != "" {
		return s.ui, tokenSourceUI
	}
	if s.env != "" {
		return s.env, tokenSourceEnv
	}
	return "", tokenSourceNone
}

// HasEnv は .env / 環境変数からトークンを読めていたかを返す
// （クリアしたときに戻る先があるかどうかの表示に使う）。
func (s *tokenStore) HasEnv() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.env != ""
}

// Set は設定画面から入力されたトークンを検証して保持する。
// changed は有効なトークンが実際に変わったかどうか
// （同じ値の再入力でセッション累計をリセットしないための判定）。
func (s *tokenStore) Set(raw string) (changed bool, err error) {
	t, err := validateToken(raw)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.effectiveLocked()
	s.ui = t
	return before != t, nil
}

// Clear は設定画面から入力されたトークンを捨て、.env 由来の値に戻す。
func (s *tokenStore) Clear() (changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.effectiveLocked()
	s.ui = ""
	return before != s.effectiveLocked()
}

// HasUI は設定画面からのトークンが入っているかを返す（クリアボタンの出し分け用）。
func (s *tokenStore) HasUI() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ui != ""
}

func (s *tokenStore) effectiveLocked() string {
	if s.ui != "" {
		return s.ui
	}
	return s.env
}

// validateToken は入力値を検証して前後の空白を落とした値を返す。
// エラーメッセージに入力値そのものを含めない（P-TOKEN-LEAK）。
func validateToken(raw string) (string, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", errors.New("トークンが空です。")
	}
	if utf8.RuneCountInString(t) > maxTokenLen {
		return "", fmt.Errorf("トークンが長すぎます（%d 文字以内）。貼り付ける範囲を確認してください。", maxTokenLen)
	}
	// Authorization ヘッダに載せる値なので、ヘッダとして正当な文字だけを通す。
	// 改行が混ざったままヘッダに入るとヘッダインジェクションになる。
	// 実際の Bearer Token は英数字と - . _ ~ + / % = だけで、この範囲に収まる。
	for _, r := range t {
		if r < 0x21 || r > 0x7e {
			return "", errors.New("トークンに使えない文字が含まれています（半角の英数字と記号のみ）。改行や空白が混ざっていないか確認してください。")
		}
	}
	return t, nil
}
