package svnclient

import (
	"bytes"
	"context" // Dodajemy import pakietu context
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout = 30 * time.Minute // Domyślny timeout dla komend SVN, teraz używany do tworzenia kontekstu
)

// SVNClientIface defines the interface for SVN operations.
type SVNClientIface interface {
	// Zmieniono: Dodano context.Context do wszystkich metod
	GetInfo(ctx context.Context, repoURL, username, password string) (string, error)
	Checkout(ctx context.Context, repoURL, localPath, username, password string) (string, error)
	Status(ctx context.Context, rootDirectory string, paths []string, username, password string) ([]SVNStatusEntry, error)
	Add(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error)
	Delete(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error)
	Commit(ctx context.Context, rootDirectory string, paths []string, message, username, password string) (string, error)
	Cleanup(ctx context.Context, localPath, username, password string) (string, error)
	Update(ctx context.Context, localPath, username, password string) (string, error)
}

// ExecSVNClient implements SVNClientIface by calling external 'svn' command.
type ExecSVNClient struct {
	svnPath string
	timeout time.Duration // Nadal przechowujemy, ale używamy do tworzenia kontekstu z timeoutem
	mu      sync.Mutex    // Mutex do serializacji wywołań SVN
}

// SVNStatusEntry represents a single entry from 'svn status --xml'.
type SVNStatusEntry struct {
	Path string `xml:"path,attr"`
	Item string `xml:"wc-status>item,attr"`
}

// NewExecSVNClient creates a new ExecSVNClient.
func NewExecSVNClient() (*ExecSVNClient, error) {
	svnPath, err := exec.LookPath("svn")
	if err != nil {
		return nil, fmt.Errorf("nie znaleziono komendy 'svn' w PATH: %w", err)
	}
	return &ExecSVNClient{
		svnPath: svnPath,
		timeout: defaultTimeout, // Ustawiamy domyślny timeout
	}, nil
}

// GetInfo retrieves information about a repository.
// Zmieniono: Dodano ctx context.Context
func (c *ExecSVNClient) GetInfo(ctx context.Context, repoURL, username, password string) (string, error) {
	return c.runCommand(ctx, "", username, password, []string{"info", repoURL})
}

// Checkout performs an SVN checkout.
// Zmieniono: Dodano ctx context.Context
func (c *ExecSVNClient) Checkout(ctx context.Context, repoURL, localPath, username, password string) (string, error) {
	if _, err := os.Stat(filepath.Join(localPath, ".svn")); err == nil {
		fmt.Printf("🔄 Lokalna ścieżka '%s' zawiera kopię roboczą SVN. Próbuję oczyścić i zaktualizować...\n", localPath)
		cleanupOutput, cleanupErr := c.Cleanup(ctx, localPath, username, password) // Przekazano ctx
		if cleanupErr != nil {
			fmt.Printf("❌ SVN cleanup dla '%s' zakończony błędem: %v\nOutput: %s\n", localPath, cleanupErr, cleanupOutput)
			return cleanupOutput, cleanupErr
		}
		fmt.Printf("✅ SVN cleanup zakończone dla '%s'.\n", localPath)
		return c.Update(ctx, localPath, username, password) // Przekazano ctx
	}

	fmt.Printf("➡️ Wykonuję SVN checkout do '%s'...\n", localPath)
	if err := os.MkdirAll(localPath, 0755); err != nil {
		return "", fmt.Errorf("nie udało się utworzyć katalogu lokalnego '%s': %w", localPath, err)
	}
	return c.runCommand(ctx, "", username, password, []string{"checkout", repoURL, localPath})
}

// Cleanup performs an SVN cleanup on the local working copy.
// Zmieniono: Dodano ctx context.Context
func (c *ExecSVNClient) Cleanup(ctx context.Context, localPath, username, password string) (string, error) {
	return c.runCommand(ctx, localPath, username, password, []string{"cleanup"})
}

// Update performs an SVN update on the local working copy.
// Zmieniono: Dodano ctx context.Context
func (c *ExecSVNClient) Update(ctx context.Context, localPath, username, password string) (string, error) {
	return c.runCommand(ctx, localPath, username, password, []string{"update", "."})
}

// Status retrieves the status of files.
// Zmieniono: Dodano ctx context.Context
func (c *ExecSVNClient) Status(ctx context.Context, rootDirectory string, paths []string, username, password string) ([]SVNStatusEntry, error) {
	args := append([]string{"status", "--xml", "--ignore-externals", "--depth", "empty"}, paths...)
	output, err := c.runCommand(ctx, rootDirectory, username, password, args) // Przekazano ctx
	if err != nil {
		return nil, fmt.Errorf("błąd wykonania statusu SVN dla %s: %w\nOutput: %s", rootDirectory, err, output)
	}

	var statusXML struct {
		Targets []struct {
			Entries []SVNStatusEntry `xml:"entry"`
		} `xml:"target"`
	}

	if err := xml.Unmarshal([]byte(output), &statusXML); err != nil {
		return nil, fmt.Errorf("błąd parsowania XML statusu SVN: %w\nOutput: %s", err, output)
	}

	var normalizedEntries []SVNStatusEntry
	for _, target := range statusXML.Targets {
		for _, entry := range target.Entries {
			relPath, relErr := filepath.Rel(rootDirectory, entry.Path)
			if relErr == nil {
				entry.Path = relPath
			}
			normalizedEntries = append(normalizedEntries, entry)
		}
	}
	return normalizedEntries, nil
}

// Add adds files/directories to version control.
// Zmieniono: Dodano ctx context.Context
func (c *ExecSVNClient) Add(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error) {
	fmt.Printf("DEBUG CLIENT.GO (Add): rootDirectory='%s', paths='%v'\n", rootDirectory, paths)
	args := append([]string{"add"}, paths...)
	return c.runCommand(ctx, rootDirectory, username, password, args) // Przekazano ctx
}

// Delete schedules files/directories for deletion.
// Zmieniono: Dodano ctx context.Context
func (c *ExecSVNClient) Delete(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error) {
	fmt.Printf("DEBUG CLIENT.GO (Delete): rootDirectory='%s', paths='%v'\n", rootDirectory, paths)
	args := append([]string{"delete"}, paths...)
	return c.runCommand(ctx, rootDirectory, username, password, args) // Przekazano ctx
}

// Commit commits changes to the repository.
// Zmieniono: Dodano ctx context.Context
func (c *ExecSVNClient) Commit(ctx context.Context, rootDirectory string, paths []string, message, username, password string) (string, error) {
	fmt.Printf("DEBUG CLIENT.GO (Commit): rootDirectory='%s', paths='%v'\n", rootDirectory, paths)
	args := append([]string{"commit", "-m", message}, paths...)
	return c.runCommand(ctx, rootDirectory, username, password, args) // Przekazano ctx
}

// runCommand executes an SVN command.
// workingDir is the directory in which the command will be run (cmd.Dir).
// args is a slice of arguments passed to the 'svn' command.
// Zmieniono: ctx context.Context jako pierwszy argument
func (c *ExecSVNClient) runCommand(ctx context.Context, workingDir string, username, password string, args []string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cmdArgs := make([]string, 0)
	if username != "" {
		cmdArgs = append(cmdArgs, "--username", username)
	}
	if password != "" {
		cmdArgs = append(cmdArgs, "--password", password)
	}
	cmdArgs = append(cmdArgs, "--non-interactive", "--no-auth-cache")

	var processedCommandArgs []string
	if len(args) > 0 {
		processedCommandArgs = append(processedCommandArgs, args[0])
		for _, arg := range args[1:] {
			if workingDir != "" && filepath.IsAbs(arg) && strings.HasPrefix(arg, workingDir) {
				relPath, err := filepath.Rel(workingDir, arg)
				if err == nil {
					processedCommandArgs = append(processedCommandArgs, relPath)
				} else {
					processedCommandArgs = append(processedCommandArgs, arg)
				}
			} else {
				processedCommandArgs = append(processedCommandArgs, arg)
			}
		}
	}
	cmdArgs = append(cmdArgs, processedCommandArgs...)

	fmt.Printf("DEBUG CLIENT.GO (runCommand): cmd.Dir='%s', svn_cmd='%s %v'\n", workingDir, c.svnPath, cmdArgs)

	// Zmieniono: Użycie contextu z timeoutem dla CommandContext
	ctxWithTimeout, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel() // Upewnij się, że kontekst zostanie anulowany po zakończeniu funkcji

	cmd := exec.CommandContext(ctxWithTimeout, c.svnPath, cmdArgs...) // Użycie CommandContext
	cmd.Dir = workingDir

	var outputBuffer bytes.Buffer
	cmd.Stdout = &outputBuffer
	cmd.Stderr = &outputBuffer

	err := cmd.Start()
	if err != nil {
		cmdName := "svn"
		if len(processedCommandArgs) > 0 {
			cmdName = processedCommandArgs[0]
		}
		return outputBuffer.String(), fmt.Errorf("nie udało się uruchomić komendy svn %q: %w", cmdName, err)
	}

	// Zmieniono: Usunięcie ręcznej logiki timeoutu, CommandContext to obsługuje
	cmdErr := cmd.Wait()
	if cmdErr != nil {
		cmdName := "svn"
		if len(processedCommandArgs) > 0 {
			cmdName = processedCommandArgs[0]
		}
		// Sprawdź, czy błąd był spowodowany anulowaniem kontekstu
		if ctxWithTimeout.Err() != nil {
			return outputBuffer.String(), fmt.Errorf("komenda svn %q została anulowana lub przekroczyła czas: %v. Output: %s", cmdName, ctxWithTimeout.Err(), outputBuffer.String())
		}
		return outputBuffer.String(), fmt.Errorf("komenda svn %q zwróciła błąd: %v. Output: %s", cmdName, cmdErr, outputBuffer.String())
	}

	return outputBuffer.String(), nil
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

// parseStatusXML parses the XML output from 'svn status --xml' into a slice of SVNStatusEntry.
func parseStatusXML(xmlData []byte) ([]SVNStatusEntry, error) {
	var statusXML struct {
		XMLName xml.Name `xml:"status"`
		Targets []struct {
			XMLName xml.Name       `xml:"target"`
			Entries []SVNStatusEntry `xml:"entry"`
		} `xml:"target"`
	}

	if err := xml.Unmarshal(xmlData, &statusXML); err != nil {
		return nil, fmt.Errorf("błąd parsowania XML statusu SVN: %w", err)
	}

	var allEntries []SVNStatusEntry
	for _, target := range statusXML.Targets {
		allEntries = append(allEntries, target.Entries...)
	}
	return allEntries, nil
}
