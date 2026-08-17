package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
	"golang.org/x/crypto/ssh"
)

func main() {
	printHostKey := flag.Bool("print-host-key", false,
		"connect only far enough to capture the switch's SSH host key, print it in host_key-ready formats, and exit; "+
			"requires only FASTIRON_HOST (no credentials). Use this when ssh-keyscan returns nothing — older FastIron "+
			"SSH stacks offer KEX algorithms modern OpenSSH refuses to negotiate.")
	flag.Parse()

	host := os.Getenv("FASTIRON_HOST")

	if *printHostKey {
		if host == "" {
			fmt.Fprintln(os.Stderr, "Set FASTIRON_HOST")
			os.Exit(1)
		}
		key, err := sshclient.FetchHostKey(host, 22, 15)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		fmt.Printf("# host_key values for %s — either format works:\n", host)
		fmt.Printf("%s %s\n", host, line)
		fmt.Printf("%s\n", ssh.FingerprintSHA256(key))
		return
	}

	user := os.Getenv("FASTIRON_USERNAME")
	pass := os.Getenv("FASTIRON_PASSWORD")

	if host == "" || user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "Set FASTIRON_HOST, FASTIRON_USERNAME, FASTIRON_PASSWORD")
		os.Exit(1)
	}

	// Use FASTIRON_ENABLE_PASSWORD if set; fall back to the login password.
	enablePass := os.Getenv("FASTIRON_ENABLE_PASSWORD")
	if enablePass == "" {
		enablePass = pass
	}

	hostKey := os.Getenv("FASTIRON_HOST_KEY")

	opts := sshclient.Options{
		Host:           host,
		Port:           22,
		Username:       user,
		Password:       pass,
		EnablePassword: enablePass,
		TimeoutSeconds: 15,
		HostKey:        hostKey,
	}

	if hostKey == "" {
		fmt.Fprintln(os.Stderr, "WARNING: FASTIRON_HOST_KEY not set — skipping SSH host key verification (MITM risk)")
		opts.InsecureSkipHostKeyVerify = true
	}

	client, err := sshclient.NewClient(opts)
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
