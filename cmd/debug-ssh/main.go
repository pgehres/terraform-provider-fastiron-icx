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
	legacySSH := flag.Bool("legacy-ssh", false,
		"additionally offer legacy SSH algorithms (SHA-1 key exchange, CBC ciphers, ssh-rsa/ssh-dss host key "+
			"signatures) for very old FastIron stacks, e.g. 08.0.30 on the ICX 6430. The -print-host-key probe "+
			"always offers them; this flag applies to the full connect.")
	transport := flag.String("transport", sshclient.TransportSSH,
		"CLI transport: ssh or telnet (FASTIRON_TRANSPORT). Telnet is PLAINTEXT and unauthenticated — lab and "+
			"competition switches that expose no SSH only; this tool connects exactly as the provider does with "+
			"transport = \"telnet\" and insecure_transport_ack = true.")
	allowEmpty := flag.Bool("allow-empty-credentials", false,
		"permit empty FASTIRON_USERNAME / FASTIRON_PASSWORD / FASTIRON_ENABLE_PASSWORD, as a factory-fresh switch "+
			"with no local users accepts (the provider's allow_empty_credentials).")
	port := flag.Int("port", 0, "TCP port; 0 = the transport's default (22 for ssh, 23 for telnet). FASTIRON_PORT is also honoured.")
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

	if env := os.Getenv("FASTIRON_TRANSPORT"); env != "" && *transport == sshclient.TransportSSH {
		*transport = env
	}
	if *port == 0 {
		if env := os.Getenv("FASTIRON_PORT"); env != "" {
			if _, err := fmt.Sscanf(env, "%d", port); err != nil {
				fmt.Fprintf(os.Stderr, "FASTIRON_PORT %q: %v\n", env, err)
				os.Exit(1)
			}
		}
	}

	user := os.Getenv("FASTIRON_USERNAME")
	pass := os.Getenv("FASTIRON_PASSWORD")

	if host == "" || (!*allowEmpty && (user == "" || pass == "")) {
		fmt.Fprintln(os.Stderr, "Set FASTIRON_HOST, FASTIRON_USERNAME, FASTIRON_PASSWORD (or pass -allow-empty-credentials for a switch with no users)")
		os.Exit(1)
	}

	// Use FASTIRON_ENABLE_PASSWORD if set; fall back to the login password.
	enablePass := os.Getenv("FASTIRON_ENABLE_PASSWORD")
	if enablePass == "" {
		enablePass = pass
	}

	hostKey := os.Getenv("FASTIRON_HOST_KEY")

	opts := sshclient.Options{
		Host:                  host,
		Port:                  *port,
		Username:              user,
		Password:              pass,
		EnablePassword:        enablePass,
		TimeoutSeconds:        15,
		HostKey:               hostKey,
		LegacySSHAlgorithms:   *legacySSH,
		Transport:             *transport,
		AllowEmptyCredentials: *allowEmpty,
	}

	switch *transport {
	case sshclient.TransportSSH:
		if hostKey == "" {
			fmt.Fprintln(os.Stderr, "WARNING: FASTIRON_HOST_KEY not set — skipping SSH host key verification (MITM risk)")
			opts.InsecureSkipHostKeyVerify = true
		}
	case sshclient.TransportTelnet:
		fmt.Fprintln(os.Stderr, "WARNING: telnet transport — credentials and configuration cross the network in plaintext, "+
			"and the switch's identity is not verified")
	default:
		fmt.Fprintf(os.Stderr, "unknown transport %q (want ssh or telnet)\n", *transport)
		os.Exit(1)
	}
	if *allowEmpty && (user == "" || pass == "" || enablePass == "") {
		fmt.Fprintln(os.Stderr, "WARNING: connecting with empty credentials")
	}

	client, err := sshclient.Connect(opts)
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
