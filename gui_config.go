package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

const guiConfigBodyLimit = 4 << 20

type guiConfigSaveRequest struct {
	Config json.RawMessage `json:"config"`
}

func guiConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !guiAuthorized(r) {
		guiWriteError(w, http.StatusForbidden, "invalid GUI request token")
		return
	}
	configFileMu.Lock()
	defer configFileMu.Unlock()

	switch r.Method {
	case http.MethodGet:
		candidate, err := guiReadConfiguration()
		if err != nil {
			guiWriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		secretFields := guiRedactConfigurationSecrets(&candidate)
		path, _ := filepath.Abs(configFile)
		startAppliesRuntime := getGUIRuntimeState() == "waiting"
		guiWriteJSON(w, http.StatusOK, map[string]interface{}{
			"config":              guiCompleteJSONValue(reflect.ValueOf(candidate)),
			"templates":           guiTemplateJSONValue(reflect.TypeOf(candidate)),
			"secretFields":        secretFields,
			"file":                path,
			"restartRequired":     true,
			"startAppliesRuntime": startAppliesRuntime,
		})
	case http.MethodPost:
		guiSaveConfiguration(w, r)
	default:
		guiMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
	}
}

func guiReadConfiguration() (configuration, error) {
	candidate := defaultConfiguration()
	if strings.TrimSpace(configFile) == "" {
		return candidate, nil
	}
	content, err := os.ReadFile(configFile)
	if errors.Is(err, os.ErrNotExist) {
		return candidate, nil
	}
	if err != nil {
		return candidate, fmt.Errorf("read settings: %w", err)
	}

	extension := strings.ToLower(filepath.Ext(configFile))
	candidate, _, err = decodeConfigurationWithLegacyFallback(string(content), extension == ".yaml" || extension == ".yml", extension == ".jsonc")
	if err != nil {
		return candidate, fmt.Errorf("parse settings for editor: %w", err)
	}
	normalizeLoadedConfiguration(&candidate)
	return candidate, nil
}

func guiSaveConfiguration(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, guiConfigBodyLimit)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request guiConfigSaveRequest
	if err := decoder.Decode(&request); err != nil {
		guiWriteError(w, http.StatusBadRequest, "invalid settings request: "+err.Error())
		return
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			guiWriteError(w, http.StatusBadRequest, "invalid settings request: multiple JSON values are not allowed")
		} else {
			guiWriteError(w, http.StatusBadRequest, "invalid settings request: "+err.Error())
		}
		return
	}
	if len(request.Config) == 0 || bytes.Equal(bytes.TrimSpace(request.Config), []byte("null")) {
		guiWriteError(w, http.StatusBadRequest, "settings payload is empty")
		return
	}

	candidate := defaultConfiguration()
	configDecoder := json.NewDecoder(bytes.NewReader(request.Config))
	configDecoder.DisallowUnknownFields()
	if err := configDecoder.Decode(&candidate); err != nil {
		guiWriteError(w, http.StatusBadRequest, "invalid setting value: "+err.Error())
		return
	}
	current, err := guiReadConfiguration()
	if err != nil {
		guiWriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	guiPreserveConfigurationSecrets(&candidate, current)
	if err := guiValidateConfiguration(candidate); err != nil {
		guiWriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	encoded, err := guiEncodeConfiguration(candidate)
	if err != nil {
		guiWriteError(w, http.StatusInternalServerError, "encode settings: "+err.Error())
		return
	}
	backup, err := guiAtomicWriteConfiguration(configFile, encoded)
	if err != nil {
		guiWriteError(w, http.StatusInternalServerError, "save settings: "+err.Error())
		return
	}
	startAppliesRuntime := getGUIRuntimeState() == "waiting"
	if !startAppliesRuntime {
		setGUIStartupIssue("settings saved; restart the application to apply every option")
	} else {
		setGUIStartupIssue("settings saved; Start loads runtime settings, while GUI launch options require a restart")
	}
	guiWriteJSON(w, http.StatusOK, map[string]interface{}{
		"ok":                  true,
		"backup":              backup,
		"restartRequired":     true,
		"startAppliesRuntime": startAppliesRuntime,
	})
}

func guiRedactConfigurationSecrets(candidate *configuration) map[string]bool {
	configured := make(map[string]bool)
	redact := func(path string, value *string, placeholders ...string) {
		trimmed := strings.TrimSpace(*value)
		isPlaceholder := false
		for _, placeholder := range placeholders {
			isPlaceholder = isPlaceholder || trimmed == placeholder
		}
		configured[path] = trimmed != "" && !isPlaceholder
		*value = ""
	}
	redact("credentials.token", &candidate.Credentials.Token, placeholderToken)
	redact("credentials.email", &candidate.Credentials.Email, placeholderEmail)
	redact("credentials.password", &candidate.Credentials.Password, placeholderPassword)
	redact("credentials.twitterPassword", &candidate.Credentials.TwitterPassword)
	redact("credentials.twitter2FA", &candidate.Credentials.Twitter2FA)
	redact("credentials.twitterAuthToken", &candidate.Credentials.TwitterAuthToken)
	redact("credentials.twitterCT0", &candidate.Credentials.TwitterCT0)
	redact("credentials.twitterProxy", &candidate.Credentials.TwitterProxy)
	redact("credentials.instagramPassword", &candidate.Credentials.InstagramPassword)
	redact("credentials.instagramProxy", &candidate.Credentials.InstagramProxy)
	redact("credentials.flickrApiKey", &candidate.Credentials.FlickrApiKey)
	if candidate.Credentials.InstagramTOTP != nil {
		value := strings.TrimSpace(*candidate.Credentials.InstagramTOTP)
		configured["credentials.instagramTOTP"] = value != ""
		candidate.Credentials.InstagramTOTP = nil
	} else {
		configured["credentials.instagramTOTP"] = false
	}
	return configured
}

func guiPreserveConfigurationSecrets(candidate *configuration, current configuration) {
	preserve := func(value *string, old string) {
		if strings.TrimSpace(*value) == "" && strings.TrimSpace(old) != "" {
			*value = old
		}
	}
	preserve(&candidate.Credentials.Token, current.Credentials.Token)
	preserve(&candidate.Credentials.Email, current.Credentials.Email)
	preserve(&candidate.Credentials.Password, current.Credentials.Password)
	preserve(&candidate.Credentials.TwitterPassword, current.Credentials.TwitterPassword)
	preserve(&candidate.Credentials.Twitter2FA, current.Credentials.Twitter2FA)
	preserve(&candidate.Credentials.TwitterAuthToken, current.Credentials.TwitterAuthToken)
	preserve(&candidate.Credentials.TwitterCT0, current.Credentials.TwitterCT0)
	preserve(&candidate.Credentials.TwitterProxy, current.Credentials.TwitterProxy)
	preserve(&candidate.Credentials.InstagramPassword, current.Credentials.InstagramPassword)
	preserve(&candidate.Credentials.InstagramProxy, current.Credentials.InstagramProxy)
	preserve(&candidate.Credentials.FlickrApiKey, current.Credentials.FlickrApiKey)
	if candidate.Credentials.InstagramTOTP == nil && current.Credentials.InstagramTOTP != nil {
		candidate.Credentials.InstagramTOTP = current.Credentials.InstagramTOTP
	}
}

func guiValidateConfiguration(candidate configuration) error {
	if candidate.DownloadWorkers < 1 || candidate.DownloadWorkers > maxConfigDownloadWorkers {
		return fmt.Errorf("downloadWorkers must be between 1 and %d", maxConfigDownloadWorkers)
	}
	if candidate.DownloadQueueSize < candidate.DownloadWorkers || candidate.DownloadQueueSize > maxConfigDownloadQueueSize {
		return fmt.Errorf("downloadQueueSize must be between downloadWorkers and %d", maxConfigDownloadQueueSize)
	}
	if candidate.DownloadTimeout < 10 || candidate.DiscordTimeout < 10 {
		return errors.New("downloadTimeout and discordTimeout must be at least 10 seconds")
	}
	if candidate.DownloadRetryMax < 1 || candidate.DownloadRetryMax > maxConfigDownloadRetry || candidate.DownloadMaxFileSize < 1 || candidate.DownloadMaxRedirects < 0 {
		return fmt.Errorf("download retry must be between 1 and %d; size and redirect limits must be valid", maxConfigDownloadRetry)
	}
	if candidate.HistoryMaxJobs < 1 || candidate.HistoryMaxJobs > maxConfigHistoryJobs || candidate.HistoryDownloadWorkers < 1 || candidate.HistoryDownloadWorkers > maxConfigHistoryDownloadWorker || candidate.HistoryRequestCount < 1 || candidate.HistoryRequestCount > 100 {
		return fmt.Errorf("historyMaxJobs must be between 1 and %d, historyDownloadWorkers between 1 and %d, and historyRequestCount between 1 and 100", maxConfigHistoryJobs, maxConfigHistoryDownloadWorker)
	}
	if err := validateLocalGUIAddress(candidate.GUIAddress); err != nil {
		return fmt.Errorf("guiAddress is invalid: %w", err)
	}
	return nil
}

func guiEncodeConfiguration(candidate configuration) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(configFile)) {
	case ".yaml", ".yml":
		return yaml.Marshal(candidate)
	default:
		return json.MarshalIndent(candidate, "", "\t")
	}
}

func guiAtomicWriteConfiguration(path string, content []byte) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("settings file path is empty")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(absPath)+".*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	backup := absPath + ".bak"
	if _, err := os.Stat(absPath); err == nil {
		stagedBackup := ""
		if _, backupErr := os.Stat(backup); backupErr == nil {
			stage, stageErr := os.CreateTemp(dir, "."+filepath.Base(absPath)+".bak.*.old")
			if stageErr != nil {
				return "", stageErr
			}
			stagedBackup = stage.Name()
			if closeErr := stage.Close(); closeErr != nil {
				_ = os.Remove(stagedBackup)
				return "", closeErr
			}
			if removeErr := os.Remove(stagedBackup); removeErr != nil {
				return "", removeErr
			}
			if renameErr := os.Rename(backup, stagedBackup); renameErr != nil {
				return "", renameErr
			}
		} else if !errors.Is(backupErr, os.ErrNotExist) {
			return "", backupErr
		}
		if err := os.Rename(absPath, backup); err != nil {
			if stagedBackup != "" {
				if restoreErr := os.Rename(stagedBackup, backup); restoreErr != nil {
					return "", fmt.Errorf("move current settings to backup: %w (previous backup remains at %q: %v)", err, stagedBackup, restoreErr)
				}
			}
			return "", err
		}
		if err := os.Rename(tmpName, absPath); err != nil {
			rollbackErr := os.Rename(backup, absPath)
			if rollbackErr == nil && stagedBackup != "" {
				if restoreErr := os.Rename(stagedBackup, backup); restoreErr != nil {
					return "", fmt.Errorf("install settings: %w (original restored, previous backup remains at %q: %v)", err, stagedBackup, restoreErr)
				}
			}
			if rollbackErr != nil {
				return "", fmt.Errorf("install settings: %w (original remains at %q; rollback failed: %v)", err, backup, rollbackErr)
			}
			return "", err
		}
		keepTemp = false
		if stagedBackup != "" {
			_ = os.Remove(stagedBackup)
		}
		return backup, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(tmpName, absPath); err != nil {
		return "", err
	}
	keepTemp = false
	return "", nil
}

// guiCompleteJSONValue includes every exported JSON field, even fields tagged
// with omitempty. This keeps the GUI editor complete as new settings are added.
func guiCompleteJSONValue(value reflect.Value) interface{} {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return guiCompleteJSONValue(value.Elem())
	}
	if value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return nil
		}
		return guiCompleteJSONValue(value.Elem())
	}

	switch value.Kind() {
	case reflect.Struct:
		result := make(map[string]interface{})
		typeOfValue := value.Type()
		for i := 0; i < value.NumField(); i++ {
			fieldType := typeOfValue.Field(i)
			if fieldType.PkgPath != "" {
				continue
			}
			name := strings.Split(fieldType.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = fieldType.Name
			}
			result[name] = guiCompleteJSONValue(value.Field(i))
		}
		return result
	case reflect.Slice, reflect.Array:
		result := make([]interface{}, value.Len())
		for i := 0; i < value.Len(); i++ {
			result[i] = guiCompleteJSONValue(value.Index(i))
		}
		return result
	case reflect.Map:
		result := make(map[string]interface{})
		iterator := value.MapRange()
		for iterator.Next() {
			result[fmt.Sprint(iterator.Key().Interface())] = guiCompleteJSONValue(iterator.Value())
		}
		return result
	default:
		return value.Interface()
	}
}

// guiTemplateJSONValue mirrors the entire settings type. Optional objects and
// empty slices therefore still have a complete template available in the UI.
func guiTemplateJSONValue(valueType reflect.Type) interface{} {
	if valueType.Kind() == reflect.Ptr {
		return guiTemplateJSONValue(valueType.Elem())
	}
	switch valueType.Kind() {
	case reflect.Struct:
		result := make(map[string]interface{})
		for i := 0; i < valueType.NumField(); i++ {
			field := valueType.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			result[name] = guiTemplateJSONValue(field.Type)
		}
		return result
	case reflect.Slice, reflect.Array:
		elementType := valueType.Elem()
		if elementType.Kind() == reflect.Struct || (elementType.Kind() == reflect.Ptr && elementType.Elem().Kind() == reflect.Struct) {
			return []interface{}{guiTemplateJSONValue(elementType)}
		}
		return []interface{}{}
	case reflect.Map:
		return map[string]interface{}{}
	default:
		return reflect.Zero(valueType).Interface()
	}
}
