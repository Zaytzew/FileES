package servertool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"filees/internal/obsandbox"
	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"
)

const (
	ExitOK          = 0
	ExitUsage       = 64
	ExitData        = 65
	ExitUnavailable = 69
	ExitSoftware    = 70
	ExitTempFail    = 75
	ExitConfig      = 78
)

const (
	readPromises   = "stdio rpath wpath flock"
	writePromises  = "stdio rpath wpath cpath fattr flock"
	mailPromises   = writePromises + " inet dns"
	workerPromises = writePromises + " inet"
)

type toolAccess struct {
	name             string
	areas            onboarding.Area
	write            bool
	needOTP          bool
	needSMTP         bool
	needWorker       bool
	needWorkerPublic bool
}

func (access toolAccess) promises() string {
	if access.needSMTP {
		return mailPromises
	}
	if access.needWorker {
		return workerPromises
	}
	if access.write {
		return writePromises
	}
	return readPromises
}

func openFiles(configPath string, access toolAccess) (*onboarding.Files, serverconfig.Config, error) {
	if err := sandboxBegin(access.promises()); err != nil {
		return nil, serverconfig.Config{}, err
	}
	var secrets serverconfig.Secrets
	if access.needOTP {
		secrets |= serverconfig.SecretOTP
	}
	if access.needSMTP {
		secrets |= serverconfig.SecretSMTP
	}
	if access.needWorker {
		secrets |= serverconfig.SecretWorker
	}
	if access.needWorkerPublic {
		secrets |= serverconfig.SecretWorkerPublic
	}
	config, err := serverconfig.LoadFor(configPath, secrets)
	if err != nil {
		return nil, serverconfig.Config{}, err
	}
	repositoryAccess := onboarding.Access{Areas: access.areas, NeedOTP: access.needOTP}
	if err := onboarding.CheckExisting(config.Root, repositoryAccess); err != nil {
		return nil, serverconfig.Config{}, err
	}
	profile := repositoryProfile(config.Root, access)
	if err := sandboxApply(profile); err != nil {
		return nil, serverconfig.Config{}, err
	}
	files, err := onboarding.OpenPrepared(config.Root, config.Onboarding, repositoryAccess)
	if err != nil {
		return nil, serverconfig.Config{}, err
	}
	return files, config, nil
}

func repositoryProfile(root string, access toolAccess) obsandbox.Profile {
	areaPerms := "r"
	if access.write {
		areaPerms = "rwc"
	}
	paths := []obsandbox.Path{{Label: "lock", Name: filepath.Join(root, ".toolchain.lock"), Perms: "rw"}}
	for _, item := range []struct {
		area       onboarding.Area
		label, dir string
	}{
		{onboarding.AreaTickets, "tickets", "tickets"},
		{onboarding.AreaOperations, "operations", "operations"},
		{onboarding.AreaAudit, "audit", "audit"},
	} {
		if access.areas&item.area != 0 {
			paths = append(paths, obsandbox.Path{Label: item.label, Name: filepath.Join(root, item.dir), Perms: areaPerms})
		}
	}
	if access.needSMTP {
		paths = append(paths,
			obsandbox.Path{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"},
			obsandbox.Path{Label: "hosts", Name: "/etc/hosts", Perms: "r"},
		)
	}
	return obsandbox.Profile{Name: access.name, Promises: access.promises(), Paths: paths}
}

var (
	sandboxBegin = obsandbox.Begin
	sandboxApply = obsandbox.Apply
)

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func report(stderr io.Writer, prefix string, err error) {
	fmt.Fprintf(stderr, "%s: %v\n", prefix, err)
}

func configPath(args []string) (string, []string, error) {
	path := "/etc/filees/server.json"
	if len(args) >= 2 && args[0] == "-config" {
		path, args = args[1], args[2:]
	}
	if !filepath.IsAbs(path) {
		return "", nil, errors.New("-config path must be absolute")
	}
	return path, args, nil
}
