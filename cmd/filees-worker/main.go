package main

import (
	"os"

	"filees/internal/servertool"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "repository-control":
			os.Exit(servertool.RunRepositoryWorker(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		case "whale-v1":
			os.Exit(servertool.RunWhaleWorker(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		case "upload-reap":
			os.Exit(servertool.RunUploadReap(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		case "upload-seed-reject":
			os.Exit(servertool.RunUploadSeedReject(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		case "mobile-onboard":
			os.Exit(servertool.RunMobileOnboardWorker(os.Stdin, os.Stdout, os.Stderr))
		case "mobile-proof":
			os.Exit(servertool.RunMobileProofWorker(os.Args[2:], os.Stdout, os.Stderr))
		case "mobile-finish":
			os.Exit(servertool.RunMobileFinishWorker(os.Args[2:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(servertool.RunWorker(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
