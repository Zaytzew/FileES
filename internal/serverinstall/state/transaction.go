package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"filees/internal/durable"
)

// Transaction is a durable write-ahead record of every pre-image needed to
// undo an apply. It is written only after all backups are complete and before
// the first target is changed.
type Transaction struct {
	Entry HistoryEntry `json:"entry"`
}

func TransactionPath(dir string) string { return filepath.Join(dir, "transaction.json") }

func LoadTransaction(dir string) (*Transaction, error) {
	data, err := os.ReadFile(TransactionPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var transaction Transaction
	if err := json.Unmarshal(data, &transaction); err != nil {
		return nil, err
	}
	if transaction.Entry.ReleaseID == "" || transaction.Entry.BackupDir == "" {
		return nil, errors.New("invalid installer transaction journal")
	}
	return &transaction, nil
}

func SaveTransaction(dir string, transaction *Transaction) error {
	if transaction == nil || transaction.Entry.ReleaseID == "" || transaction.Entry.BackupDir == "" {
		return errors.New("invalid installer transaction journal")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(dir, ".transaction-*.tmp")
	if err != nil {
		return err
	}
	tmp := temp.Name()
	defer os.Remove(tmp)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, TransactionPath(dir)); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}

func RemoveTransaction(dir string) error {
	if err := os.Remove(TransactionPath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return durable.SyncDirectory(dir)
}
