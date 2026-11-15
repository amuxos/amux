package bridge

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/amuxos/amux/internal/amuxhome"
)

// Secrets holds Lark (Feishu) application credentials and the owner's open_id.
// Stored in a profile-specific secrets file with mode 0600.
type Secrets struct {
	AppId        string `json:"app_id"`
	AppSecret    string `json:"app_secret"`
	OpenId       string `json:"open_id"`        // bridge owner's Feishu open_id for DM notifications
	NotifyChatId string `json:"notify_chat_id"` // optional: Feishu group chat_id (oc_xxx) for 话题 thread notifications
	BotName      string `json:"bot_name"`       // optional: Feishu bot display name used as mention fallback
	BotOpenId    string `json:"bot_open_id"`    // optional: stable Feishu bot open_id/app id for mention matching
}

// CarrySecretsForApp returns a new Secrets for a setup targeting appID,
// carrying over only the fields from existing that remain valid. Feishu owner
// open_id and bot name/open_id are scoped to a single app: under a different
// app_id they identify nobody, so when the app changes they are dropped and
// must be re-established (device-flow scan identity, first-DM owner capture,
// bot identity auto-fetch). NotifyChatId is tenant-scoped and survives. The
// caller fills AppSecret.
func CarrySecretsForApp(existing *Secrets, appID string) *Secrets {
	s := &Secrets{AppId: appID}
	if existing == nil {
		return s
	}
	s.NotifyChatId = existing.NotifyChatId
	// Only a definite app switch (non-empty old app_id, different new one)
	// invalidates the ids; with no recorded app_id their provenance is unknown
	// and the v0.25 "rerun keeps owner" rule applies.
	oldApp := strings.TrimSpace(existing.AppId)
	if oldApp == "" || strings.TrimSpace(appID) == oldApp {
		s.OpenId = existing.OpenId
		s.BotName = existing.BotName
		s.BotOpenId = existing.BotOpenId
	}
	return s
}

const DefaultProfileName = "default"

var validProfileName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func DefaultSecretsDir() string {
	return filepath.Join(amuxhome.SecretsDir(), "feishu")
}

// ProfileSecretsPath returns the secrets path for a Feishu bot profile.
func ProfileSecretsPath(profile string) (string, error) {
	if !validProfileName.MatchString(profile) {
		return "", fmt.Errorf("invalid Feishu profile %q", profile)
	}
	return filepath.Join(DefaultSecretsDir(), profile+".json"), nil
}

// BridgeIdFromAppId returns the stable Feishu bridge namespace for an app_id.
func BridgeIdFromAppId(appID string) string {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return ""
	}
	return "feishu:" + appID
}

// LoadProfileSecrets reads credentials for a Feishu bot profile.
func LoadProfileSecrets(profile string) (*Secrets, error) {
	path, err := ProfileSecretsPath(profile)
	if err != nil {
		return nil, err
	}
	return LoadSecrets(path)
}

// SaveProfileSecrets writes credentials for a Feishu bot profile.
func SaveProfileSecrets(profile string, s *Secrets) error {
	path, err := ProfileSecretsPath(profile)
	if err != nil {
		return err
	}
	return SaveSecrets(path, s)
}

// DisableProfileSecrets disables a Feishu bot profile by renaming its secrets file.
func DisableProfileSecrets(profile string) (string, error) {
	path, err := ProfileSecretsPath(profile)
	if err != nil {
		return "", err
	}
	disabledPath := path + ".disabled"
	if err := os.Rename(path, disabledPath); err != nil {
		return disabledPath, err
	}
	return disabledPath, nil
}

const disabledSecretsSuffix = ".json.disabled"

// DisabledProfileSecretsPath is where DisableProfileSecrets keeps credentials.
func DisabledProfileSecretsPath(profile string) (string, error) {
	path, err := ProfileSecretsPath(profile)
	if err != nil {
		return "", err
	}
	return path + ".disabled", nil
}

// CreateProfileSecrets writes credentials for a new profile and fails with
// fs.ErrExist when the profile already has enabled or disabled credentials.
func CreateProfileSecrets(profile string, s *Secrets) error {
	path, err := ProfileSecretsPath(profile)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path + ".disabled"); err == nil {
		return fmt.Errorf("Feishu profile %s is disabled: %w", profile, fs.ErrExist)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	return file.Close()
}

// EnableProfileSecrets restores credentials moved aside by DisableProfileSecrets.
// It never replaces credentials configured for the profile in the meantime.
func EnableProfileSecrets(profile string) error {
	path, err := ProfileSecretsPath(profile)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("Feishu profile %s is already enabled: %w", profile, fs.ErrExist)
	}
	if _, err := os.Stat(path + ".disabled"); err != nil {
		return err
	}
	return os.Rename(path+".disabled", path)
}

// DeleteDisabledProfileSecrets permanently removes disabled credentials. An
// enabled profile must be disabled first.
func DeleteDisabledProfileSecrets(profile string) error {
	path, err := DisabledProfileSecretsPath(profile)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// ListDisabledSecretProfiles lists Feishu bot profiles with disabled credentials.
func ListDisabledSecretProfiles() ([]string, error) {
	entries, err := os.ReadDir(DefaultSecretsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var profiles []string
	for _, entry := range entries {
		profile, ok := strings.CutSuffix(entry.Name(), disabledSecretsSuffix)
		if entry.IsDir() || !ok || !validProfileName.MatchString(profile) {
			continue
		}
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	return profiles, nil
}

// ListSecretProfiles lists configured Feishu bot profiles.
func ListSecretProfiles() ([]string, error) {
	entries, err := os.ReadDir(DefaultSecretsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var profiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		profile := strings.TrimSuffix(name, ".json")
		if !validProfileName.MatchString(profile) {
			continue
		}
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	return profiles, nil
}

// LoadSecrets reads and parses the secrets file.
func LoadSecrets(path string) (*Secrets, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Secrets
	return &s, json.Unmarshal(data, &s)
}

// SaveSecrets writes secrets to the file with mode 0600.
func SaveSecrets(path string, s *Secrets) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
