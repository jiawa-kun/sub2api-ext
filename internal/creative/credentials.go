package creative

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"sub2api-ext/internal/store"
)

const maxUserAPIKeyLength = 4096

type UserCredentialOption struct {
	ProviderID      int64  `json:"provider_id"`
	ProviderName    string `json:"provider_name"`
	ProviderKind    string `json:"provider_kind"`
	Configured      bool   `json:"configured"`
	KeyHint         string `json:"key_hint,omitempty"`
	AvailableModels int    `json:"available_models,omitempty"`
	EncryptionReady bool   `json:"encryption_ready"`
}

func deriveCredentialKey(secret string) []byte {
	secret = strings.TrimSpace(secret)
	if len(secret) < 32 {
		return nil
	}
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func credentialAAD(userID, providerID int64) []byte {
	return []byte(strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(providerID, 10))
}

func encryptCredential(key []byte, userID, providerID int64, plaintext string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("用户 API Key 加密未配置")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), credentialAAD(userID, providerID))
	return "v1:" + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func decryptCredential(key []byte, userID, providerID int64, encoded string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("用户 API Key 加密未配置")
	}
	if !strings.HasPrefix(encoded, "v1:") {
		return "", fmt.Errorf("用户 API Key 密文版本不受支持")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, "v1:"))
	if err != nil {
		return "", fmt.Errorf("用户 API Key 密文无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("用户 API Key 密文无效")
	}
	plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], credentialAAD(userID, providerID))
	if err != nil {
		return "", fmt.Errorf("用户 API Key 解密失败")
	}
	return string(plaintext), nil
}

func credentialHint(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4 {
		return "****"
	}
	return "****" + value[len(value)-4:]
}

func (s *Service) CredentialOptions(ctx context.Context, userID int64) ([]UserCredentialOption, error) {
	providers, err := s.store.ListCreativeProviders(ctx)
	if err != nil {
		return nil, err
	}
	credentials, err := s.store.ListCreativeUserCredentials(ctx, userID)
	if err != nil {
		return nil, err
	}
	byProvider := map[int64]store.CreativeUserCredential{}
	for _, value := range credentials {
		byProvider[value.ProviderID] = value
	}
	out := []UserCredentialOption{}
	for _, provider := range providers {
		if provider.Kind != ProviderPool || !provider.Enabled {
			continue
		}
		value, configured := byProvider[provider.ID]
		out = append(out, UserCredentialOption{
			ProviderID: provider.ID, ProviderName: provider.Name, ProviderKind: provider.Kind,
			Configured: configured, KeyHint: value.KeyHint, EncryptionReady: len(s.credentialKey) == 32,
		})
	}
	return out, nil
}

func (s *Service) SaveUserCredential(ctx context.Context, userID, providerID int64, apiKey string) (*UserCredentialOption, error) {
	apiKey = strings.TrimSpace(apiKey)
	if userID <= 0 || providerID <= 0 {
		return nil, fmt.Errorf("用户或渠道无效")
	}
	if len(s.credentialKey) != 32 {
		return nil, fmt.Errorf("服务端未配置 CREATIVE_CREDENTIAL_SECRET")
	}
	if len(apiKey) < 8 || len(apiKey) > maxUserAPIKeyLength {
		return nil, fmt.Errorf("API Key 长度无效")
	}
	if err := s.ensureCredentialMutable(ctx, userID, providerID); err != nil {
		return nil, err
	}
	provider, err := s.store.GetCreativeProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if !provider.Enabled || provider.Kind != ProviderPool {
		return nil, fmt.Errorf("该渠道不接受用户 API Key")
	}
	callProvider := *provider
	callProvider.APIKey = apiKey
	remote, err := s.listRemoteModels(ctx, callProvider)
	if err != nil {
		return nil, fmt.Errorf("API Key 验证失败: %w", err)
	}
	accounts, err := s.discoverAccountModels(ctx, *provider)
	if err != nil {
		return nil, fmt.Errorf("账号池模型读取失败: %w", err)
	}
	available := 0
	for _, modelID := range remote {
		if accounts[modelID] > 0 {
			available++
		}
	}
	if available == 0 {
		return nil, fmt.Errorf("该 API Key 没有可用的账号池媒体模型")
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if err := s.ensureCredentialMutable(ctx, userID, providerID); err != nil {
		return nil, err
	}
	current, err := s.store.GetCreativeProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if !current.Enabled || current.Kind != ProviderPool {
		return nil, fmt.Errorf("该渠道不接受用户 API Key")
	}
	if current.SourceGroup != provider.SourceGroup {
		return nil, fmt.Errorf("账号池配置已变化，请重试")
	}
	provider = current
	ciphertext, err := encryptCredential(s.credentialKey, userID, providerID, apiKey)
	if err != nil {
		return nil, err
	}
	saved, err := s.store.SaveCreativeUserCredential(ctx, store.CreativeUserCredential{
		UserID: userID, ProviderID: providerID, APIKeyCipher: ciphertext, KeyHint: credentialHint(apiKey),
	})
	if err != nil {
		return nil, err
	}
	return &UserCredentialOption{
		ProviderID: provider.ID, ProviderName: provider.Name, ProviderKind: provider.Kind,
		Configured: true, KeyHint: saved.KeyHint, AvailableModels: available, EncryptionReady: true,
	}, nil
}

func (s *Service) DeleteUserCredential(ctx context.Context, userID, providerID int64) error {
	if userID <= 0 || providerID <= 0 {
		return fmt.Errorf("用户或渠道无效")
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if err := s.ensureCredentialMutable(ctx, userID, providerID); err != nil {
		return err
	}
	return s.store.DeleteCreativeUserCredential(ctx, userID, providerID)
}

func (s *Service) ensureCredentialMutable(ctx context.Context, userID, providerID int64) error {
	active, err := s.store.HasActiveCreativeVideo(ctx, userID, providerID)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("该渠道有视频任务生成中，完成后才能更换或删除 API Key")
	}
	return nil
}

func (s *Service) providerForUser(ctx context.Context, userID int64, provider store.CreativeProvider) (store.CreativeProvider, error) {
	if provider.Kind != ProviderPool {
		return provider, nil
	}
	if userID <= 0 {
		return provider, fmt.Errorf("用户无效")
	}
	value, err := s.store.GetCreativeUserCredential(ctx, userID, provider.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return provider, fmt.Errorf("请先配置自己的 Sub2API API Key")
		}
		return provider, err
	}
	apiKey, err := decryptCredential(s.credentialKey, userID, provider.ID, value.APIKeyCipher)
	if err != nil {
		return provider, err
	}
	provider.APIKey = apiKey
	return provider, nil
}
