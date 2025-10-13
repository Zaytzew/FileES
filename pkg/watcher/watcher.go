package watcher

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings" // Dodajemy brakujący import pakietu strings
	"sync"
	"time"
)

const (
	FileAdded    EventType = iota
	FileModified
	FileDeleted
)

type EventType int

func (et EventType) String() string {
	switch et {
	case FileAdded:
		return "FileAdded"
	case FileModified:
		return "FileModified"
	case FileDeleted:
		return "FileDeleted"
	default:
		return "Unknown"
	}
}

// FileMetadata przechowuje metadane pliku.
type FileMetadata struct {
	MD5   string    `json:"md5"`
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime"`
}

// Event reprezentuje zdarzenie wykryte przez watchera.
type Event struct {
	Type EventType
	Path string // Pełna ścieżka do pliku
	MD5  string // Suma kontrolna MD5 pliku w momencie zdarzenia
}

// Watcher monitoruje zmiany w katalogu.
type Watcher struct {
	rootPath      string
	interval      time.Duration
	state         map[string]FileMetadata
	stateFilePath string
	mu            sync.Mutex
}

// NewWatcher tworzy nowego Watchera.
func NewWatcher(rootPath string, interval time.Duration, stateFilePath string) (*Watcher, error) {
	if _, err := os.Stat(rootPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("katalog główny watchera '%s' nie istnieje", rootPath)
	}
	return &Watcher{
		rootPath:      rootPath,
		interval:      interval,
		state:         make(map[string]FileMetadata),
		stateFilePath: stateFilePath,
	}, nil
}

// StartWithStop rozpoczyna monitorowanie katalogu i nasłuchuje sygnału zatrzymania przez context.
func (w *Watcher) StartWithStop(eventChan chan<- Event, ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	fmt.Printf("🔍 Rozpoczynam monitorowanie katalogu '%s' co %v...\n", w.rootPath, w.interval)

	// Wykonaj początkowe skanowanie zaraz po starcie
	w.scanDirectory(eventChan)

	for {
		select {
		case <-ticker.C:
			w.scanDirectory(eventChan)
		case <-ctx.Done():
			fmt.Println("Wykryto sygnał zatrzymania Watchera. Zapisuję stan i kończę działanie.")
			// Zapisanie stanu przed wyjściem jest obsługiwane w main.go po zamknięciu wszystkich goroutine
			return
		}
	}
}

// scanDirectory skanuje katalog i wykrywa zmiany.
func (w *Watcher) scanDirectory(eventChan chan<- Event) {
	w.mu.Lock()
	defer w.mu.Unlock()

	newState := make(map[string]FileMetadata)
	deletedFiles := make(map[string]FileMetadata)
	for path, metadata := range w.state {
		deletedFiles[path] = metadata // Na początku zakładamy, że wszystkie stare pliki zostały usunięte
	}

	err := filepath.Walk(w.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Printf("⚠️ Błąd dostępu do pliku/katalogu '%s': %v\n", path, err)
			return nil // Kontynuuj chodzenie po drzewie, pomimo błędu
		}

		if info.IsDir() {
			return nil // Pomijamy katalogi
		}

		// Ignoruj pliki w katalogu .svn
		if strings.Contains(path, filepath.Join(w.rootPath, ".svn")) {
			return nil
		}

		// Oblicz MD5 tylko dla plików, które potencjalnie się zmieniły lub są nowe
		oldMetadata, exists := w.state[path]
		var currentMD5 string
		if exists && info.ModTime().Equal(oldMetadata.Mtime) && info.Size() == oldMetadata.Size {
			currentMD5 = oldMetadata.MD5 // Użyj starego MD5, jeśli czas modyfikacji i rozmiar się nie zmieniły
		} else {
			// Oblicz MD5 tylko jeśli plik jest nowy lub potencjalnie zmieniony
			md5sum, md5Err := CalculateMD5(path)
			if md5Err != nil {
				fmt.Printf("❌ Błąd obliczania MD5 dla '%s': %v\n", path, md5Err)
				return nil
			}
			currentMD5 = md5sum
		}

		newMetadata := FileMetadata{
			MD5:   currentMD5,
			Size:  info.Size(),
			Mtime: info.ModTime(),
		}
		newState[path] = newMetadata

		if !exists {
			eventChan <- Event{Type: FileAdded, Path: path, MD5: currentMD5}
		} else {
			if oldMetadata.MD5 != newMetadata.MD5 {
				eventChan <- Event{Type: FileModified, Path: path, MD5: currentMD5}
			}
			delete(deletedFiles, path) // Plik nadal istnieje, więc nie jest usunięty
		}
		return nil
	})

	if err != nil {
		fmt.Printf("❌ Błąd podczas skanowania katalogu '%s': %v\n", w.rootPath, err)
	}

	// Wyślij zdarzenia dla usuniętych plików
	for path, metadata := range deletedFiles {
		eventChan <- Event{Type: FileDeleted, Path: path, MD5: metadata.MD5}
	}

	w.state = newState // Zaktualizuj stan Watchera
}

// SaveState zapisuje aktualny stan Watchera do pliku.
func (w *Watcher) SaveState() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := json.Marshal(w.state)
	if err != nil {
		return fmt.Errorf("nie udało się zserializować stanu watchera: %w", err)
	}

	// Upewnij się, że katalog istnieje
	dir := filepath.Dir(w.stateFilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("nie udało się utworzyć katalogu dla pliku stanu watchera '%s': %w", dir, err)
	}

	err = os.WriteFile(w.stateFilePath, data, 0644)
	if err != nil {
		return fmt.Errorf("nie udało się zapisać stanu watchera do pliku '%s': %w", w.stateFilePath, err)
	}
	fmt.Printf("✅ Stan watchera zapisany do: %s\n", w.stateFilePath)
	return nil
}

// LoadState wczytuje poprzedni stan Watchera z pliku.
func (w *Watcher) LoadState() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(w.stateFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("plik stanu watchera '%s' nie istnieje", w.stateFilePath)
		}
		return fmt.Errorf("nie udało się odczytać pliku stanu watchera '%s': %w", w.stateFilePath, err)
	}

	var loadedState map[string]FileMetadata
	if err := json.Unmarshal(data, &loadedState); err != nil {
		return fmt.Errorf("błąd parsowania pliku stanu watchera '%s': %w", w.stateFilePath, err)
	}

	w.state = loadedState
	return nil
}

// CalculateMD5 oblicza sumę MD5 dla pliku.
func CalculateMD5(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
