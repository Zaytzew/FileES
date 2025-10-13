// Wklej zawartość pkg/config/config.go tutaj
package config

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RepoConfig reprezentuje konfigurację pojedynczego repozytorium do synchronizacji.
type RepoConfig struct {
	ID            string `json:"id"`
	RepoURL       string `json:"repo_url"`
	LocalPath     string `json:"local_path"`
	WatchInterval string `json:"watch_interval"`  // np. "5s"
	CommitInterval string `json:"commit_interval"` // np. "30s"
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	HashID        string `json:"-"` // Generowany automatycznie, nie zapisywany do JSON
}

// LoadConfig wczytuje konfigurację wielu repozytoriów z pliku JSON.
func LoadConfig(filePath string) ([]RepoConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []RepoConfig{}, fmt.Errorf("plik konfiguracyjny '%s' nie istnieje. Stwórz go.", filePath)
		}
		return nil, fmt.Errorf("nie udało się odczytać pliku konfiguracyjnego '%s': %w", filePath, err)
	}

	var configs []RepoConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, fmt.Errorf("błąd parsowania pliku konfiguracyjnego JSON '%s': %w", filePath, err)
	}

	// Uzupełnij HashID dla każdego repozytorium
	for i := range configs {
		configs[i].HashID = GenerateHashID(configs[i].RepoURL, configs[i].LocalPath)
		configs[i].LocalPath = filepath.Clean(configs[i].LocalPath) // Normalizuj ścieżkę
	}

	return configs, nil
}

// GenerateHashID generuje unikalny HashID na podstawie URL repozytorium i ścieżki lokalnej.
// Używany do unikalnej identyfikacji repozytoriów, np. w nazwach plików stanu watchera.
func GenerateHashID(repoURL, localPath string) string {
	hasher := md5.New()
	hasher.Write([]byte(repoURL + "_" + localPath)) // Łączymy, aby zapewnić unikalność
	return hex.EncodeToString(hasher.Sum(nil))
}

// Przykład struktury pliku config.json:
/*
[
  {
    "id": "MyProject",
    "repo_url": "svn://cloud.atmprojekt.pl/MyProject-files",
    "local_path": "/home/acme/MyProjectSync",
    "watch_interval": "5s",
    "commit_interval": "30s",
    "username": "acme",
    "password": "twojehaslo"
  }
]
*/
