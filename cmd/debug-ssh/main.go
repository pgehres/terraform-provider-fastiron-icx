package main

import (
	"fmt"
	"os"

	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

func main() {
	host := os.Getenv("FASTIRON_HOST")
	user := os.Getenv("FASTIRON_USERNAME")
	pass := os.Getenv("FASTIRON_PASSWORD")

	if host == "" || user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "Set FASTIRON_HOST, FASTIRON_USERNAME, FASTIRON_PASSWORD")
		os.Exit(1)
	}

	client, err := sshclient.NewClient(sshclient.Options{
		Host:           host,
		Port:           22,
		Username:       user,
		Password:       pass,
		EnablePassword: pass,
		TimeoutSeconds: 15,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = client.Close() }()

	output, err := client.GetRunningConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "get config error: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(output)
}
